package harness

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// APIProxy sits between the manager and the API server. It authenticates
// upstream as the manager identity, so RBAC and the audit log see the manager,
// and it lets the harness act on individual requests: most importantly,
// SIGKILL the manager the instant a create succeeds, before the manager sees
// the response. That makes "crash right after the first child is created" a
// deterministic crash point instead of a race.
type APIProxy struct {
	URL string

	srv *http.Server

	mu       sync.Mutex
	onCreate func(method, path string) // fires once, then disarms
	latency  time.Duration
}

// isCrashableCreate reports whether a request is a create of a real object
// (not an Event, Lease, or review), i.e. a point where a crash matters.
func isCrashableCreate(method, path string, code int) bool {
	if method != http.MethodPost || code < 200 || code > 299 {
		return false
	}
	for _, skip := range []string{"/events", "/leases", "reviews", "/tokenrequests", "/token"} {
		if strings.Contains(path, skip) {
			return false
		}
	}
	return true
}

// startAPIProxy serves plain HTTP on 127.0.0.1 and forwards to upstream using
// upstream's credentials.
func startAPIProxy(upstream *rest.Config) (*APIProxy, error) {
	rt, err := rest.TransportFor(upstream)
	if err != nil {
		return nil, err
	}
	target, err := url.Parse(upstream.Host)
	if err != nil {
		return nil, err
	}
	if target.Scheme == "" {
		target.Scheme = "https"
	}
	p := &APIProxy{}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
			pr.Out.Header.Del("Authorization") // the transport adds the manager's credentials
		},
		Transport:     rt,
		FlushInterval: -1, // stream watches
		ModifyResponse: func(resp *http.Response) error {
			if isCrashableCreate(resp.Request.Method, resp.Request.URL.Path, resp.StatusCode) {
				p.mu.Lock()
				hook := p.onCreate
				p.onCreate = nil
				p.mu.Unlock()
				if hook != nil {
					hook(resp.Request.Method, resp.Request.URL.Path)
				}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() == nil {
				w.WriteHeader(http.StatusBadGateway)
			}
		},
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		d := p.latency
		p.mu.Unlock()
		if d > 0 {
			time.Sleep(d)
		}
		rp.ServeHTTP(w, r)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p.srv = &http.Server{Handler: handler, ReadHeaderTimeout: 30 * time.Second}
	p.URL = "http://" + ln.Addr().String()
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// ArmCreateCrash calls fn once, synchronously, right after the next successful
// create of a non-Event object, before the response reaches the manager.
func (p *APIProxy) ArmCreateCrash(fn func(method, path string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onCreate = fn
}

// Disarm clears a pending crash hook.
func (p *APIProxy) Disarm() { p.ArmCreateCrash(nil) }

// SetLatency adds delay to every manager request (a slow API server).
func (p *APIProxy) SetLatency(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.latency = d
}

// Close stops the proxy.
func (p *APIProxy) Close(ctx context.Context) error { return p.srv.Shutdown(ctx) }

// writeProxyKubeconfig writes a kubeconfig that points at the proxy with no credentials.
func writeProxyKubeconfig(dir, server string) (string, error) {
	kc := clientcmdapi.NewConfig()
	kc.Clusters["gauntlet"] = &clientcmdapi.Cluster{Server: server}
	kc.AuthInfos["manager"] = &clientcmdapi.AuthInfo{}
	kc.Contexts["manager"] = &clientcmdapi.Context{Cluster: "gauntlet", AuthInfo: "manager"}
	kc.CurrentContext = "manager"
	path := filepath.Join(dir, "manager-via-proxy.kubeconfig")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return path, clientcmd.WriteToFile(*kc, path)
}
