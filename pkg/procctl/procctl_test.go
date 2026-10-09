package procctl

import (
	"testing"
	"time"
)

func sleeper() Spec { return Spec{Binary: "/bin/sh", Args: []string{"-c", "sleep 30"}} }

func TestKillIsNotUnexpected(t *testing.T) {
	m := New(sleeper())
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	if !m.Running() {
		t.Fatal("should be running")
	}
	if err := m.Kill(); err != nil {
		t.Fatal(err)
	}
	if m.Running() {
		t.Fatal("should have stopped")
	}
	if n := len(m.UnexpectedExits()); n != 0 {
		t.Fatalf("harness kill counted as unexpected: %d", n)
	}
}

func TestRestartCountsStarts(t *testing.T) {
	m := New(sleeper())
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	if err := m.Restart(); err != nil {
		t.Fatal(err)
	}
	defer m.Kill()
	if m.Starts() != 2 || !m.Running() {
		t.Fatalf("starts=%d running=%v", m.Starts(), m.Running())
	}
}

func TestCrashIsUnexpected(t *testing.T) {
	m := New(Spec{Binary: "/bin/sh", Args: []string{"-c", "exit 3"}})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for m.Running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	ex := m.UnexpectedExits()
	if len(ex) != 1 || ex[0].ExitCode != 3 {
		t.Fatalf("unexpected exits = %+v", ex)
	}
}

func TestStopFallsBackToKill(t *testing.T) {
	// A process that ignores SIGTERM still goes away.
	m := New(Spec{Binary: "/bin/sh", Args: []string{"-c", "trap '' TERM; sleep 30"}})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if testing.Short() {
		_ = m.Kill()
		return
	}
	_ = m.Stop() // returns an error after the fallback; that's expected
	if m.Running() {
		t.Fatal("process survived Stop")
	}
}

func TestRestartOnCrash(t *testing.T) {
	m := New(Spec{Binary: "/bin/sh", Args: []string{"-c", "sleep 0.2; exit 1"}, RestartOnCrash: true, RestartDelay: 50 * time.Millisecond})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for m.Starts() < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	_ = m.Stop()
	if m.Starts() < 3 {
		t.Fatalf("expected at least 3 starts from crash restarts, got %d", m.Starts())
	}
	if len(m.UnexpectedExits()) < 2 {
		t.Fatalf("crashes should still be recorded: %v", m.UnexpectedExits())
	}
	n := m.Starts()
	time.Sleep(400 * time.Millisecond)
	if m.Starts() != n || m.Running() {
		t.Fatal("Stop should disable auto-restart")
	}
}
