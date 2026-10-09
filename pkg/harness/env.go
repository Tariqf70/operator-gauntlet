// Package harness starts a Kubernetes control plane (envtest or an existing kind
// cluster), installs the operator's CRDs, runs the operator's manager binary as
// a least-privileged user, and exposes what the checks need: an admin client,
// the audit log, the fake cloud, and crash/restart control.
//
// NOTE: this package was written without network access to fetch Kubernetes Go
// modules, so it has not been compiled yet. Run `go mod tidy && go vet ./...`
// first and fix any API drift against your controller-runtime version.
package harness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/Tariqf70/operator-gauntlet/pkg/audit"
	"github.com/Tariqf70/operator-gauntlet/pkg/contract"
	"github.com/Tariqf70/operator-gauntlet/pkg/fakecloud"
	"github.com/Tariqf70/operator-gauntlet/pkg/procctl"
)

// ManagerUser is the identity the operator runs as in envtest mode.
const ManagerUser = "gauntlet:manager"

// Mode selects the control plane.
type Mode string

const (
	ModeEnvtest Mode = "envtest" // kube-apiserver + etcd only; fast; no controllers, so no GC
	ModeKind    Mode = "kind"    // a real cluster from kind/cluster.yaml; GC and APF available
)

// Options configure a harness run.
type Options struct {
	OperatorDir string
	Contract    *contract.Contract
	Mode        Mode
	WorkDir     string    // scratch dir for audit policy, logs, kubeconfigs
	Log         io.Writer // harness and manager logs

	// Kind mode only.
	KindKubeconfig string // admin kubeconfig for the kind cluster
	KindAuditLog   string // host path of the API server audit log (see kind/cluster.yaml)
}

// Env is a running harness.
type Env struct {
	Opts     Options
	GVK      schema.GroupVersionKind
	Scheme   *runtime.Scheme
	AdminCfg *rest.Config
	Admin    client.Client

	ManagerUsername   string
	ManagerKubeconfig string
	Manager           *procctl.Manager

	// API sits between the manager and the API server (crash points, latency).
	API *APIProxy

	Cloud    *fakecloud.Server // nil unless the contract uses the fake cloud
	CloudURL string

	auditLog string
	testEnv  *envtest.Environment
	cloudSrv *http.Server
	started  time.Time
}

const auditPolicy = `apiVersion: audit.k8s.io/v1
kind: Policy
omitStages: ["RequestReceived"]
rules:
- level: Metadata
`

// Start brings everything up and builds the manager, but does not start it.
// On error, anything already started is torn down.
func Start(ctx context.Context, o Options) (_ *Env, err error) {
	if o.Log == nil {
		o.Log = io.Discard
	}
	if err := os.MkdirAll(o.WorkDir, 0o755); err != nil {
		return nil, err
	}
	c := o.Contract
	e := &Env{
		Opts:    o,
		GVK:     schema.GroupVersionKind{Group: c.API.Group, Version: c.API.Version, Kind: c.API.Kind},
		Scheme:  runtime.NewScheme(),
		started: time.Now(),
	}
	defer func() {
		if err != nil {
			_ = e.Stop()
		}
	}()
	if err := clientgoscheme.AddToScheme(e.Scheme); err != nil {
		return nil, err
	}
	if err := apiextensionsv1.AddToScheme(e.Scheme); err != nil {
		return nil, err
	}

	crdDir := filepath.Join(o.OperatorDir, "config", "crd", "bases")
	switch o.Mode {
	case ModeEnvtest:
		if err := e.startEnvtest(crdDir); err != nil {
			return nil, err
		}
	case ModeKind:
		if err := e.startKind(ctx, crdDir); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown mode %q", o.Mode)
	}

	// Route the manager through the API proxy, authenticated upstream as the manager.
	mgrCfg, err := clientcmd.BuildConfigFromFlags("", e.ManagerKubeconfig)
	if err != nil {
		return nil, err
	}
	if e.API, err = startAPIProxy(mgrCfg); err != nil {
		return nil, err
	}
	if e.ManagerKubeconfig, err = writeProxyKubeconfig(o.WorkDir, e.API.URL); err != nil {
		return nil, err
	}

	if e.Admin, err = client.New(e.AdminCfg, client.Options{Scheme: e.Scheme}); err != nil {
		return nil, err
	}

	if c.UsesFakeCloud {
		if err := e.startCloud(); err != nil {
			return nil, err
		}
	}

	fmt.Fprintf(o.Log, "building manager in %s\n", o.OperatorDir)
	bin, err := procctl.Build(ctx, o.OperatorDir, o.Log)
	if err != nil {
		return nil, err
	}
	env := []string{"KUBECONFIG=" + e.ManagerKubeconfig}
	if e.CloudURL != "" {
		env = append(env, "FAKECLOUD_URL="+e.CloudURL)
	}
	if gc := os.Getenv("GOCOVERDIR"); gc != "" {
		env = append(env, "GOCOVERDIR="+gc)
	}
	e.Manager = procctl.New(procctl.Spec{Binary: bin, Args: procctl.DefaultArgs, Env: env, Log: o.Log, RestartOnCrash: true})
	return e, nil
}

func (e *Env) startEnvtest(crdDir string) error {
	policy := filepath.Join(e.Opts.WorkDir, "audit-policy.yaml")
	if err := os.WriteFile(policy, []byte(auditPolicy), 0o644); err != nil {
		return err
	}
	e.auditLog = filepath.Join(e.Opts.WorkDir, "audit.log")
	_ = os.Remove(e.auditLog)

	e.testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{crdDir},
		ErrorIfCRDPathMissing: true,
		// BinaryAssetsDirectory defaults to $KUBEBUILDER_ASSETS (set it with setup-envtest).
	}
	api := e.testEnv.ControlPlane.GetAPIServer()
	api.Configure().
		Set("audit-policy-file", policy).
		Set("audit-log-path", e.auditLog).
		Set("audit-log-mode", "blocking").
		// Explicit, so the API server starts in sandboxes that have no default route.
		Set("advertise-address", "127.0.0.1")

	cfg, err := e.testEnv.Start()
	if err != nil {
		return fmt.Errorf("envtest: %w (is KUBEBUILDER_ASSETS set? run: setup-envtest use -p path)", err)
	}
	e.AdminCfg = cfg

	user, err := e.testEnv.AddUser(envtest.User{Name: ManagerUser}, nil)
	if err != nil {
		return fmt.Errorf("envtest add user: %w", err)
	}
	kc, err := user.KubeConfig()
	if err != nil {
		return err
	}
	e.ManagerUsername = ManagerUser
	e.ManagerKubeconfig = filepath.Join(e.Opts.WorkDir, "manager.kubeconfig")
	if err := os.WriteFile(e.ManagerKubeconfig, kc, 0o600); err != nil {
		return err
	}
	return nil
}

func (e *Env) startKind(ctx context.Context, crdDir string) error {
	if e.Opts.KindKubeconfig == "" || e.Opts.KindAuditLog == "" {
		return errors.New("kind mode needs --kubeconfig and --audit-log (see kind/README.md)")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", e.Opts.KindKubeconfig)
	if err != nil {
		return err
	}
	e.AdminCfg = cfg
	e.auditLog = e.Opts.KindAuditLog
	adm, err := client.New(cfg, client.Options{Scheme: e.Scheme})
	if err != nil {
		return err
	}
	if err := installCRDs(ctx, adm, crdDir); err != nil {
		return err
	}
	// The manager runs as a ServiceAccount whose token we mint.
	username, kubeconfig, err := serviceAccountKubeconfig(ctx, cfg, adm, e.Opts.WorkDir)
	if err != nil {
		return err
	}
	e.ManagerUsername, e.ManagerKubeconfig = username, kubeconfig
	return nil
}

func (e *Env) startCloud() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	e.Cloud = fakecloud.New()
	e.cloudSrv = &http.Server{Handler: e.Cloud, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = e.cloudSrv.Serve(ln) }()
	e.CloudURL = "http://" + ln.Addr().String()
	return nil
}

// BindManagerRBAC grants the manager identity exactly the rules from the
// operator's config/rbac/role.yaml, and returns the parsed roles for R7.
func (e *Env) BindManagerRBAC(ctx context.Context) ([]ParsedRole, error) {
	if err := cleanupBindings(ctx, e.Admin); err != nil {
		return nil, err
	}
	if err := e.assertRBACEnforced(ctx); err != nil {
		return nil, err
	}
	roles, err := ParseRoleFile(filepath.Join(e.Opts.OperatorDir, "config", "rbac", "role.yaml"))
	if err != nil {
		return nil, err
	}
	return roles, bindRoles(ctx, e.Admin, roles, e.Opts.Mode, e.ManagerUsername)
}

// assertRBACEnforced checks that the manager identity starts with no access.
// If the API server isn't enforcing RBAC, the least-privilege results would be
// meaningless, so the run stops.
func (e *Env) assertRBACEnforced(ctx context.Context) error {
	cfg, err := clientcmd.BuildConfigFromFlags("", e.ManagerKubeconfig)
	if err != nil {
		return err
	}
	c, err := client.New(cfg, client.Options{Scheme: e.Scheme})
	if err != nil {
		return err
	}
	err = c.List(ctx, &corev1.SecretList{}, client.InNamespace("default"))
	if apierrors.IsForbidden(err) {
		return nil
	}
	if err == nil {
		return errors.New("the manager identity can list Secrets before any RBAC is bound: the API server is not enforcing RBAC (check envtest's --authorization-mode)")
	}
	return fmt.Errorf("RBAC sanity check: %w", err)
}

// AuditEvents reads the audit log so far.
func (e *Env) AuditEvents() ([]audit.Event, error) { return audit.ReadFile(e.auditLog) }

// Stop tears everything down.
func (e *Env) Stop() error {
	var errs []error
	if e.Manager != nil {
		errs = append(errs, e.Manager.Stop())
	}
	if e.cloudSrv != nil {
		errs = append(errs, e.cloudSrv.Close())
	}
	if e.API != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = e.API.Close(ctx) // open watches make a clean shutdown slow; ignore
		cancel()
	}
	if e.testEnv != nil {
		errs = append(errs, e.testEnv.Stop())
	}
	return errors.Join(errs...)
}
