package rbaccheck

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Tariqf70/operator-gauntlet/pkg/contract"
)

func webapp(t *testing.T) *contract.Contract {
	_, file, _, _ := runtime.Caller(0)
	c, err := contract.Load(filepath.Join(filepath.Dir(file), "..", "..", "specs", "webapp"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCleanRolePasses(t *testing.T) {
	roles := []Role{{Kind: "ClusterRole", Name: "manager-role", Rules: []Rule{
		{APIGroups: []string{"bench.example.com"}, Resources: []string{"webapps"}, Verbs: []string{"get", "list", "watch", "update", "patch"}},
		{APIGroups: []string{"bench.example.com"}, Resources: []string{"webapps/status"}, Verbs: []string{"get", "update", "patch"}},
		{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		{APIGroups: []string{""}, Resources: []string{"services"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
	}}}
	if f := Check(roles, webapp(t)); len(f) != 0 {
		t.Fatalf("unexpected findings: %v", f)
	}
}

func TestWildcardsAndExtraResources(t *testing.T) {
	roles := []Role{{Kind: "ClusterRole", Name: "manager-role", Rules: []Rule{
		{APIGroups: []string{"*"}, Resources: []string{"*"}, Verbs: []string{"*"}},
		{APIGroups: []string{""}, Resources: []string{"secrets", "services"}, Verbs: []string{"get"}},
	}}}
	f := Check(roles, webapp(t))
	var s []string
	for _, x := range f {
		s = append(s, x.Problem)
	}
	joined := strings.Join(s, "\n")
	for _, want := range []string{`wildcard apiGroups "*"`, `wildcard resources "*"`, `wildcard verbs "*"`, "core/secrets"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing finding %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "core/services") {
		t.Errorf("services is allowed for webapp but was flagged:\n%s", joined)
	}
}
