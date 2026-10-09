// Package procctl builds and runs the operator's manager binary as a child
// process, so the harness can SIGKILL it at crash points and restart it.
package procctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Build runs `go build -o bin/manager ./cmd` in the operator directory and
// returns the binary path.
func Build(ctx context.Context, operatorDir string, out io.Writer) (string, error) {
	bin := filepath.Join(operatorDir, "bin", "manager")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd")
	cmd.Dir = operatorDir
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("build failed: %w", err)
	}
	return bin, nil
}

// Spec describes how to start the manager.
type Spec struct {
	Binary string
	Args   []string
	Env    []string // appended to a minimal environment
	Log    io.Writer

	// RestartOnCrash restarts the process after an exit the harness didn't
	// cause, the way kubelet restarts a crashed pod. Crashes are still recorded.
	RestartOnCrash bool
	RestartDelay   time.Duration // default 1s
}

// DefaultArgs are the flags every spec promises the manager will receive.
var DefaultArgs = []string{"--leader-elect=false", "--metrics-bind-address=0", "--health-probe-bind-address=0"}

// Manager supervises one manager process at a time.
type Manager struct {
	spec Spec

	mu         sync.Mutex
	cmd        *exec.Cmd
	done       chan struct{}
	starts     int
	killed     bool // the harness killed or stopped the current process on purpose
	closed     bool // Stop was called: never auto-restart again until Start
	unexpected []ExitInfo
}

// ExitInfo records an exit the harness did not cause.
type ExitInfo struct {
	At       time.Time `json:"at"`
	ExitCode int       `json:"exitCode"`
	Err      string    `json:"err"`
}

// New returns a Manager; call Start to launch.
func New(spec Spec) *Manager {
	if spec.Log == nil {
		spec.Log = io.Discard
	}
	return &Manager{spec: spec}
}

// Start launches the process. It is an error to start while one is running.
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != nil {
		return errors.New("manager already running")
	}
	cmd := exec.Command(m.spec.Binary, m.spec.Args...)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}, m.spec.Env...)
	cmd.Stdout, cmd.Stderr = m.spec.Log, m.spec.Log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // so Kill reaches children too
	if err := cmd.Start(); err != nil {
		return err
	}
	m.cmd, m.done, m.killed, m.closed = cmd, make(chan struct{}), false, false
	m.starts++
	done := m.done
	go func() {
		err := cmd.Wait()
		m.mu.Lock()
		if !m.killed {
			info := ExitInfo{At: time.Now(), ExitCode: cmd.ProcessState.ExitCode()}
			if err != nil {
				info.Err = err.Error()
			}
			m.unexpected = append(m.unexpected, info)
		}
		crashed := !m.killed
		if m.cmd == cmd {
			m.cmd = nil
		}
		restart := crashed && m.spec.RestartOnCrash && !m.closed
		m.mu.Unlock()
		close(done)
		if restart {
			delay := m.spec.RestartDelay
			if delay == 0 {
				delay = time.Second
			}
			time.Sleep(delay)
			m.mu.Lock()
			skip := m.closed || m.cmd != nil
			m.mu.Unlock()
			if !skip {
				_ = m.Start()
			}
		}
	}()
	return nil
}

func (m *Manager) signal(sig syscall.Signal, wait time.Duration) error {
	m.mu.Lock()
	cmd, done := m.cmd, m.done
	if cmd == nil {
		m.mu.Unlock()
		return nil
	}
	m.killed = true
	m.mu.Unlock()
	// Negative PID signals the whole process group.
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	select {
	case <-done:
		return nil
	case <-time.After(wait):
		if sig != syscall.SIGKILL {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
			return fmt.Errorf("manager ignored %v for %v; killed", sig, wait)
		}
		return fmt.Errorf("manager did not exit %v after SIGKILL", wait)
	}
}

// Kill sends SIGKILL (a crash) and waits for the process to exit.
func (m *Manager) Kill() error { return m.signal(syscall.SIGKILL, 10*time.Second) }

// Stop sends SIGTERM and waits up to 15s, then SIGKILL. It also disables
// RestartOnCrash until the next Start.
func (m *Manager) Stop() error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	return m.signal(syscall.SIGTERM, 15*time.Second)
}

// Restart kills the process (if running) and starts it again.
func (m *Manager) Restart() error {
	if err := m.Kill(); err != nil {
		return err
	}
	return m.Start()
}

// Running reports whether a process is alive.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cmd != nil
}

// Starts is how many times the process was started.
func (m *Manager) Starts() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.starts
}

// UnexpectedExits lists exits the harness did not cause (crashes, os.Exit on errors).
func (m *Manager) UnexpectedExits() []ExitInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ExitInfo(nil), m.unexpected...)
}
