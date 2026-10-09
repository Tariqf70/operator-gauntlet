// Package rbaccheck scores an operator's requested RBAC rules against a
// contract's allow-list (rule R7). It works on already-parsed rules so it has
// no Kubernetes dependencies; the harness parses config/rbac/role.yaml.
package rbaccheck

import (
	"fmt"
	"sort"

	"github.com/Tariqf70/operator-gauntlet/pkg/contract"
)

// Rule mirrors rbacv1.PolicyRule.
type Rule struct {
	APIGroups       []string `json:"apiGroups"`
	Resources       []string `json:"resources"`
	ResourceNames   []string `json:"resourceNames,omitempty"`
	Verbs           []string `json:"verbs"`
	NonResourceURLs []string `json:"nonResourceURLs,omitempty"`
}

// Role is a parsed Role or ClusterRole.
type Role struct {
	Kind      string `json:"kind"` // Role | ClusterRole
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Rules     []Rule `json:"rules"`
}

// Finding is one violation.
type Finding struct {
	Role    string `json:"role"`
	Problem string `json:"problem"`
}

func (f Finding) String() string { return f.Role + ": " + f.Problem }

// Check returns violations of least privilege: wildcards anywhere, non-resource
// URLs, and resources outside the contract's allow-list.
func Check(roles []Role, c *contract.Contract) []Finding {
	var out []Finding
	seen := map[string]bool{}
	add := func(role, problem string) {
		key := role + "|" + problem
		if !seen[key] {
			seen[key] = true
			out = append(out, Finding{Role: role, Problem: problem})
		}
	}
	for _, r := range roles {
		name := r.Kind + "/" + r.Name
		for _, rule := range r.Rules {
			for _, g := range rule.APIGroups {
				if g == "*" {
					add(name, `wildcard apiGroups "*"`)
				}
			}
			for _, res := range rule.Resources {
				if res == "*" || res == "*/*" {
					add(name, `wildcard resources "`+res+`"`)
				}
			}
			for _, v := range rule.Verbs {
				if v == "*" {
					add(name, `wildcard verbs "*"`)
				}
			}
			if len(rule.NonResourceURLs) > 0 {
				add(name, fmt.Sprintf("nonResourceURLs %v not needed by the contract", rule.NonResourceURLs))
			}
			for _, g := range rule.APIGroups {
				if g == "*" {
					continue
				}
				for _, res := range rule.Resources {
					if res == "*" || res == "*/*" {
						continue
					}
					if !c.Allowed(g, res) {
						gg := g
						if gg == "" {
							gg = "core"
						}
						add(name, "requests "+gg+"/"+res+", which the contract doesn't need")
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}
