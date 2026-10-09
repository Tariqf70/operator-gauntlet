package contract

import (
	"path/filepath"
	"runtime"
	"testing"
)

func specsDir(t *testing.T) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "specs")
}

func TestLoadShippedContracts(t *testing.T) {
	for _, name := range []string{"configsync", "manageddatabase", "webapp"} {
		c, err := Load(filepath.Join(specsDir(t), name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if c.Spec != name {
			t.Errorf("%s: spec field is %q", name, c.Spec)
		}
		if !c.Allowed(c.API.Group, c.API.Resource+"/status") {
			t.Errorf("%s: status subresource must be allowed", name)
		}
	}
}

func TestSecretsOnlyForManagedDatabase(t *testing.T) {
	for name, want := range map[string]bool{"configsync": false, "manageddatabase": true, "webapp": false} {
		c, err := Load(filepath.Join(specsDir(t), name))
		if err != nil {
			t.Fatal(err)
		}
		if got := c.Allowed("", "secrets"); got != want {
			t.Errorf("%s: secrets allowed = %v, want %v", name, got, want)
		}
	}
}

func TestRulesForHidesR3R6(t *testing.T) {
	visible := RulesFor(true)
	if len(visible) != 5 {
		t.Fatalf("visible rules = %v", visible)
	}
	for _, r := range visible {
		if r == "R3" || r == "R6" {
			t.Fatalf("hidden rule %s leaked into visible set", r)
		}
	}
	if len(RulesFor(false)) != 7 {
		t.Fatal("full rule set should have 7 rules")
	}
}

func TestValidateRejectsUnclassifiedRule(t *testing.T) {
	c := &Contract{Spec: "x", API: API{Group: "g", Version: "v", Kind: "K", Resource: "ks"}, CrashPoint: CrashFirstChild,
		Rules: map[string]string{"R1": "stated"}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for missing rule classifications")
	}
}
