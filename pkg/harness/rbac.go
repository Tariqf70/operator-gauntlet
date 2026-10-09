package harness

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/Tariqf70/operator-gauntlet/pkg/rbaccheck"
)

// ParsedRole is a Role or ClusterRole from the operator's RBAC manifests.
type ParsedRole struct {
	Kind      string
	Name      string
	Namespace string
	Rules     []rbacv1.PolicyRule
}

// ForCheck converts to the dependency-free form used by rbaccheck.
func (p ParsedRole) ForCheck() rbaccheck.Role {
	r := rbaccheck.Role{Kind: p.Kind, Name: p.Name, Namespace: p.Namespace}
	for _, pr := range p.Rules {
		r.Rules = append(r.Rules, rbaccheck.Rule{
			APIGroups: pr.APIGroups, Resources: pr.Resources, ResourceNames: pr.ResourceNames,
			Verbs: pr.Verbs, NonResourceURLs: pr.NonResourceURLs,
		})
	}
	return r
}

var docSep = regexp.MustCompile(`(?m)^---\s*$`)

// ParseRoleFile reads every Role/ClusterRole document in a multi-document YAML file.
func ParseRoleFile(path string) ([]ParsedRole, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w (controller-gen writes it on `make manifests`)", path, err)
	}
	var out []ParsedRole
	for _, doc := range docSep.Split(string(b), -1) {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var meta struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Rules []rbacv1.PolicyRule `json:"rules"`
		}
		if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if meta.Kind != "Role" && meta.Kind != "ClusterRole" {
			continue
		}
		out = append(out, ParsedRole{Kind: meta.Kind, Name: meta.Metadata.Name, Namespace: meta.Metadata.Namespace, Rules: meta.Rules})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no Role or ClusterRole found", path)
	}
	return out, nil
}

func subjectFor(mode Mode, username string) rbacv1.Subject {
	if mode == ModeKind {
		// system:serviceaccount:<ns>:<name>
		parts := strings.Split(username, ":")
		return rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: parts[2], Name: parts[3]}
	}
	return rbacv1.Subject{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: username}
}

// managedLabel marks RBAC objects the harness creates, so reruns can remove them.
var managedLabel = map[string]string{"gauntlet.example.com/managed": "true"}

// cleanupBindings deletes RBAC objects left by earlier runs (kind clusters are reused).
func cleanupBindings(ctx context.Context, c client.Client) error {
	sel := client.MatchingLabels(managedLabel)
	crbs := &rbacv1.ClusterRoleBindingList{}
	if err := c.List(ctx, crbs, sel); err != nil {
		return err
	}
	for i := range crbs.Items {
		if err := c.Delete(ctx, &crbs.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	crs := &rbacv1.ClusterRoleList{}
	if err := c.List(ctx, crs, sel); err != nil {
		return err
	}
	for i := range crs.Items {
		if err := c.Delete(ctx, &crs.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	rbs := &rbacv1.RoleBindingList{}
	if err := c.List(ctx, rbs, sel); err != nil {
		return err
	}
	for i := range rbs.Items {
		if err := c.Delete(ctx, &rbs.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	rs := &rbacv1.RoleList{}
	if err := c.List(ctx, rs, sel); err != nil {
		return err
	}
	for i := range rs.Items {
		if err := c.Delete(ctx, &rs.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// bindRoles creates copies of the operator's roles and binds them to the manager identity.
func bindRoles(ctx context.Context, c client.Client, roles []ParsedRole, mode Mode, username string) error {
	subj := subjectFor(mode, username)
	for i, r := range roles {
		name := fmt.Sprintf("gauntlet-manager-%d", i)
		switch r.Kind {
		case "ClusterRole":
			cr := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: managedLabel}, Rules: r.Rules}
			crb := &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: name, Labels: managedLabel},
				RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: name},
				Subjects:   []rbacv1.Subject{subj},
			}
			if err := createOrReplace(ctx, c, cr); err != nil {
				return err
			}
			if err := createOrReplace(ctx, c, crb); err != nil {
				return err
			}
		case "Role":
			ns := r.Namespace
			if ns == "" || ns == "system" {
				// Kustomize placeholder; the harness doesn't run kustomize, so skip and let
				// the checks report any resulting 403s.
				continue
			}
			if err := ensureNamespace(ctx, c, ns); err != nil {
				return err
			}
			role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: managedLabel}, Rules: r.Rules}
			rb := &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: managedLabel},
				RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name},
				Subjects:   []rbacv1.Subject{subj},
			}
			if err := createOrReplace(ctx, c, role); err != nil {
				return err
			}
			if err := createOrReplace(ctx, c, rb); err != nil {
				return err
			}
		}
	}
	return nil
}

func createOrReplace(ctx context.Context, c client.Client, obj client.Object) error {
	err := c.Create(ctx, obj)
	if apierrors.IsAlreadyExists(err) {
		if err := c.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		obj.SetResourceVersion("")
		return c.Create(ctx, obj)
	}
	return err
}

// applyCRD creates the CRD or updates it in place. (Deleting a CRD is
// asynchronous and would also wipe its objects, so never delete and recreate.)
func applyCRD(ctx context.Context, c client.Client, crd *apiextensionsv1.CustomResourceDefinition) error {
	existing := &apiextensionsv1.CustomResourceDefinition{}
	err := c.Get(ctx, client.ObjectKeyFromObject(crd), existing)
	if apierrors.IsNotFound(err) {
		return c.Create(ctx, crd)
	}
	if err != nil {
		return err
	}
	crd.SetResourceVersion(existing.GetResourceVersion())
	return c.Update(ctx, crd)
}

func ensureNamespace(ctx context.Context, c client.Client, name string) error {
	err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// installCRDs applies every CRD in dir and waits until each is Established (kind mode).
func installCRDs(ctx context.Context, c client.Client, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no CRDs in %s (run `make manifests` in the operator)", dir)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		for _, doc := range docSep.Split(string(b), -1) {
			if strings.TrimSpace(doc) == "" {
				continue
			}
			crd := &apiextensionsv1.CustomResourceDefinition{}
			if err := yaml.Unmarshal(bytes.TrimSpace([]byte(doc)), crd); err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
			if err := applyCRD(ctx, c, crd); err != nil {
				return err
			}
			if err := wait.PollUntilContextTimeout(ctx, 250*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
				got := &apiextensionsv1.CustomResourceDefinition{}
				if err := c.Get(ctx, client.ObjectKeyFromObject(crd), got); err != nil {
					return false, nil
				}
				for _, cond := range got.Status.Conditions {
					if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
						return true, nil
					}
				}
				return false, nil
			}); err != nil {
				return fmt.Errorf("CRD %s not established: %w", crd.Name, err)
			}
		}
	}
	return nil
}

// serviceAccountKubeconfig creates gauntlet-system/gauntlet-manager and writes a
// kubeconfig using a short-lived token for it (kind mode).
func serviceAccountKubeconfig(ctx context.Context, admin *rest.Config, c client.Client, workDir string) (string, string, error) {
	const ns, name = "gauntlet-system", "gauntlet-manager"
	if err := ensureNamespace(ctx, c, ns); err != nil {
		return "", "", err
	}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	if err := c.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", "", err
	}
	cs, err := kubernetes.NewForConfig(admin)
	if err != nil {
		return "", "", err
	}
	exp := int64(4 * 3600)
	tr, err := cs.CoreV1().ServiceAccounts(ns).CreateToken(ctx, name,
		&authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: &exp}}, metav1.CreateOptions{})
	if err != nil {
		return "", "", err
	}
	cluster := &clientcmdapi.Cluster{Server: admin.Host, CertificateAuthorityData: admin.TLSClientConfig.CAData, CertificateAuthority: admin.TLSClientConfig.CAFile}
	kc := clientcmdapi.NewConfig()
	kc.Clusters["kind"] = cluster
	kc.AuthInfos["manager"] = &clientcmdapi.AuthInfo{Token: tr.Status.Token}
	kc.Contexts["manager"] = &clientcmdapi.Context{Cluster: "kind", AuthInfo: "manager"}
	kc.CurrentContext = "manager"
	path := filepath.Join(workDir, "manager.kubeconfig")
	if err := clientcmd.WriteToFile(*kc, path); err != nil {
		return "", "", err
	}
	return "system:serviceaccount:" + ns + ":" + name, path, nil
}
