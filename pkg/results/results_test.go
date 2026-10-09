package results

import (
	"path/filepath"
	"strings"
	"testing"
)

func b(v bool) *bool { return &v }

func sampleRuns() []Run {
	return []Run{
		{Setup: "S1", BuildOK: true, OwnTestsPassed: b(true), Rules: []RuleResult{
			{ID: "R1", Pass: false, Stated: true}, {ID: "R3", Pass: true, Hidden: true}, {ID: "R5", Skipped: true}}},
		{Setup: "S1", BuildOK: true, OwnTestsPassed: b(true), Rules: []RuleResult{
			{ID: "R1", Pass: true, Stated: true}, {ID: "R3", Pass: true, Hidden: true}}},
		{Setup: "S4", BuildOK: true, OwnTestsPassed: b(false), Rules: []RuleResult{
			{ID: "R1", Pass: true, Stated: true}, {ID: "R3", Pass: false, Hidden: true}}},
		{Setup: "S4", BuildOK: false},
	}
}

func TestHeadline(t *testing.T) {
	broke, own := Headline(sampleRuns())
	if broke != 1 || own != 2 {
		t.Fatalf("headline = %d of %d, want 1 of 2", broke, own)
	}
}

func TestByRuleAndSetup(t *testing.T) {
	setups, table := ByRuleAndSetup(sampleRuns(), []string{"R1", "R3", "R5"})
	if strings.Join(setups, ",") != "S1,S4" {
		t.Fatalf("setups = %v", setups)
	}
	if got := table["R1"]["S1"]; got != (Tally{1, 2}) {
		t.Errorf("R1/S1 = %v", got)
	}
	// Failed build counts as a fail.
	if got := table["R1"]["S4"]; got != (Tally{1, 2}) {
		t.Errorf("R1/S4 = %v", got)
	}
	// Skipped rule excluded, failed build still counted.
	if got := table["R5"]["S1"]; got != (Tally{0, 0}) {
		t.Errorf("R5/S1 = %v", got)
	}
	if got := table["R5"]["S4"]; got != (Tally{0, 1}) {
		t.Errorf("R5/S4 = %v", got)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := sampleRuns()[0]
	r.Spec, r.Model = "webapp", "model-x"
	if err := r.Save(filepath.Join(dir, "a.json")); err != nil {
		t.Fatal(err)
	}
	runs, err := LoadAll(filepath.Join(dir, "*.json"))
	if err != nil || len(runs) != 1 || runs[0].Model != "model-x" {
		t.Fatalf("runs=%v err=%v", runs, err)
	}
}

func TestMarkdownMentionsHidden(t *testing.T) {
	md := Markdown(sampleRuns(), []string{"R1", "R3"}, map[string]bool{"R3": true})
	if !strings.Contains(md, "R3 (hidden)") || !strings.Contains(md, "**1 of 2**") {
		t.Fatalf("markdown:\n%s", md)
	}
}
