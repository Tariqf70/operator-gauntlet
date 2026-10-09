package audit

import (
	"strings"
	"testing"
	"time"
)

const sample = `{"stage":"RequestReceived","verb":"patch","user":{"username":"gauntlet:manager"},"objectRef":{"resource":"webapps","apiGroup":"bench.example.com","name":"a","namespace":"n"},"requestReceivedTimestamp":"2026-10-01T00:00:01Z"}
{"stage":"ResponseComplete","verb":"patch","user":{"username":"gauntlet:manager"},"objectRef":{"resource":"webapps","apiGroup":"bench.example.com","name":"a","namespace":"n"},"responseStatus":{"code":200},"requestReceivedTimestamp":"2026-10-01T00:00:01Z"}
{"stage":"ResponseComplete","verb":"update","user":{"username":"gauntlet:manager"},"objectRef":{"resource":"webapps","subresource":"status","apiGroup":"bench.example.com","name":"a","namespace":"n"},"responseStatus":{"code":200},"requestReceivedTimestamp":"2026-10-01T00:00:30Z"}
{"stage":"ResponseComplete","verb":"update","user":{"username":"gauntlet:manager"},"objectRef":{"resource":"webapps","subresource":"status","apiGroup":"bench.example.com","name":"a","namespace":"n"},"responseStatus":{"code":409},"requestReceivedTimestamp":"2026-10-01T00:00:45Z"}
{"stage":"ResponseComplete","verb":"get","user":{"username":"gauntlet:manager"},"objectRef":{"resource":"deployments","apiGroup":"apps","name":"a","namespace":"n"},"responseStatus":{"code":200},"requestReceivedTimestamp":"2026-10-01T00:00:50Z"}
{"stage":"ResponseComplete","verb":"list","user":{"username":"gauntlet:manager"},"objectRef":{"resource":"secrets","apiGroup":""},"responseStatus":{"code":403},"requestReceivedTimestamp":"2026-10-01T00:00:55Z"}
{"stage":"ResponseComplete","verb":"create","user":{"username":"admin"},"objectRef":{"resource":"webapps","apiGroup":"bench.example.com","name":"a","namespace":"n"},"responseStatus":{"code":201},"requestReceivedTimestamp":"2026-10-01T00:00:00Z"}
{"stage":"ResponseComplete","verb":"patch","user":{"username":"gauntlet:m`

func load(t *testing.T) []Event {
	ev, skipped, err := Read(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 1 {
		t.Fatalf("expected the truncated last line to be skipped, skipped=%d", skipped)
	}
	return ev
}

func TestWritesCountsOnlyCompletedWritesForUser(t *testing.T) {
	ev := load(t)
	if got := Writes(ev, Filter{User: "gauntlet:manager"}); got != 3 {
		t.Fatalf("writes = %d, want 3 (patch, update, conflicting update)", got)
	}
	if got := Writes(ev, Filter{User: "admin"}); got != 1 {
		t.Fatalf("admin writes = %d, want 1", got)
	}
}

func TestWindowedWritesPerMinute(t *testing.T) {
	ev := load(t)
	since := time.Date(2026, 10, 1, 0, 0, 30, 0, time.UTC)
	until := since.Add(30 * time.Second)
	f := Filter{User: "gauntlet:manager", Since: since, Until: until}
	if got := Writes(ev, f); got != 2 {
		t.Fatalf("windowed writes = %d, want 2", got)
	}
	if got := WritesPerMinute(ev, f); got != 4 {
		t.Fatalf("writes/min = %v, want 4", got)
	}
}

func TestStatusWritesSplit(t *testing.T) {
	s := StatusWrites(load(t), "gauntlet:manager", "webapps")
	if s.MainResource != 1 || s.Subresource != 1 {
		t.Fatalf("split = %+v, want main=1 sub=1 (the 409 is not counted)", s)
	}
}

func TestForbiddenAndConflicts(t *testing.T) {
	ev := load(t)
	fb := Forbidden(ev, Filter{User: "gauntlet:manager"})
	if fb["list core/secrets"] != 1 || len(fb) != 1 {
		t.Fatalf("forbidden = %v", fb)
	}
	if c := Conflicts(ev, Filter{User: "gauntlet:manager"}); c != 1 {
		t.Fatalf("conflicts = %d", c)
	}
}
