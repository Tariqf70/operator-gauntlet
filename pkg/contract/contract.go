// Package contract loads a spec's acceptance contract (specs/<name>/contract.json).
package contract

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// AllRules is the full rule set, in report order.
var AllRules = []string{"R1", "R2", "R3", "R4", "R5", "R6", "R7"}

// HiddenRules are never shown to an agent running the suite for feedback (setup S4).
var HiddenRules = map[string]bool{"R3": true, "R6": true}

// Crash points supported by the harness.
const (
	CrashFakeCloudCreate = "fakecloud-create"   // kill the manager the instant the fake cloud stores a Create
	CrashFirstChild      = "first-child-create" // kill the manager when its first child object is created
)

// API identifies the custom resource the operator must serve.
type API struct {
	Group      string `json:"group"`
	Version    string `json:"version"`
	Kind       string `json:"kind"`
	Resource   string `json:"resource"`
	Namespaced bool   `json:"namespaced"`
}

// RBACAllow lists the resources (with optional subresource, e.g. "foos/status") an
// operator may request for one API group.
type RBACAllow struct {
	APIGroup  string   `json:"apiGroup"`
	Resources []string `json:"resources"`
}

// Contract is one spec's acceptance contract.
type Contract struct {
	Spec                          string            `json:"spec"`
	API                           API               `json:"api"`
	UsesFakeCloud                 bool              `json:"usesFakeCloud"`
	CrashPoint                    string            `json:"crashPoint"`
	SteadyStateMaxWritesPerMinute float64           `json:"steadyStateMaxWritesPerMinute"`
	Rules                         map[string]string `json:"rules"` // rule ID -> "stated" | "implicit"
	RBACAllow                     []RBACAllow       `json:"rbacAllow"`

	Dir string `json:"-"` // directory the contract was loaded from
}

// Load reads contract.json from a spec directory and validates it.
func Load(specDir string) (*Contract, error) {
	path := filepath.Join(specDir, "contract.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Contract
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Dir = specDir
	return &c, c.Validate()
}

// Validate checks that every rule is classified and the crash point is known.
func (c *Contract) Validate() error {
	if c.Spec == "" || c.API.Kind == "" || c.API.Resource == "" || c.API.Group == "" || c.API.Version == "" {
		return fmt.Errorf("contract: spec and api.{group,version,kind,resource} are required")
	}
	for _, r := range AllRules {
		switch c.Rules[r] {
		case "stated", "implicit":
		default:
			return fmt.Errorf("contract %s: rule %s must be \"stated\" or \"implicit\", got %q", c.Spec, r, c.Rules[r])
		}
	}
	switch c.CrashPoint {
	case CrashFakeCloudCreate, CrashFirstChild:
	default:
		return fmt.Errorf("contract %s: unknown crashPoint %q", c.Spec, c.CrashPoint)
	}
	if c.CrashPoint == CrashFakeCloudCreate && !c.UsesFakeCloud {
		return fmt.Errorf("contract %s: crashPoint %q requires usesFakeCloud", c.Spec, c.CrashPoint)
	}
	return nil
}

// Stated reports whether the spec tells the agent about the rule.
func (c *Contract) Stated(rule string) bool { return c.Rules[rule] == "stated" }

// RulesFor returns the rules to run. With visibleOnly, hidden rules are dropped.
func RulesFor(visibleOnly bool) []string {
	var out []string
	for _, r := range AllRules {
		if visibleOnly && HiddenRules[r] {
			continue
		}
		out = append(out, r)
	}
	return out
}

// Allowed reports whether the contract allows RBAC on apiGroup/resource.
// resource may include a subresource ("foos/status").
func (c *Contract) Allowed(apiGroup, resource string) bool {
	for _, a := range c.RBACAllow {
		if a.APIGroup != apiGroup {
			continue
		}
		for _, r := range a.Resources {
			if r == resource {
				return true
			}
		}
	}
	return false
}

// AllowedList renders the allow-list for messages, sorted.
func (c *Contract) AllowedList() []string {
	var out []string
	for _, a := range c.RBACAllow {
		for _, r := range a.Resources {
			g := a.APIGroup
			if g == "" {
				g = "core"
			}
			out = append(out, g+"/"+r)
		}
	}
	sort.Strings(out)
	return out
}
