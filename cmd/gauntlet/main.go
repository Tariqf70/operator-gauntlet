// Command gauntlet fault-tests a Kubebuilder operator against the rules in RULES.md.
//
//	gauntlet run    --spec specs/webapp --operator ./out/webapp-S1-modelA-1 [--visible-only] [--out result.json]
//	gauntlet rbac   --spec specs/webapp --operator ./out/webapp-S1-modelA-1
//	gauntlet report results/*.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Tariqf70/operator-gauntlet/pkg/checks"
	"github.com/Tariqf70/operator-gauntlet/pkg/contract"
	"github.com/Tariqf70/operator-gauntlet/pkg/harness"
	"github.com/Tariqf70/operator-gauntlet/pkg/rbaccheck"
	"github.com/Tariqf70/operator-gauntlet/pkg/results"
)

func usage() {
	fmt.Fprintln(os.Stderr, "usage: gauntlet run|rbac|report [flags]  (see README.md)")
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(os.Args[2:])
	case "rbac":
		err = cmdRBAC(os.Args[2:])
	case "report":
		err = cmdReport(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gauntlet:", err)
		os.Exit(1)
	}
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	spec := fs.String("spec", "", "spec directory (contains contract.json)")
	op := fs.String("operator", ".", "operator project directory (Kubebuilder v4 layout)")
	mode := fs.String("mode", "envtest", "envtest | kind")
	visible := fs.Bool("visible-only", false, "run and show only the visible rules (setup S4 feedback)")
	out := fs.String("out", "", "write the result JSON here")
	workdir := fs.String("workdir", "", "scratch directory (default: a temp dir)")
	logPath := fs.String("log", "", "write harness and manager logs here (default: stderr)")
	setup := fs.String("setup", "", "setup label for the result (S1..S4)")
	model := fs.String("model", "", "model ID for the result")
	agent := fs.String("agent", "", "agent/tool name and version for the result")
	attempt := fs.Int("attempt", 1, "attempt number for the result")
	ownTests := fs.String("own-tests", "unknown", "did the project's own `make test` pass? true | false | unknown")
	kubeconfig := fs.String("kubeconfig", "", "kind mode: admin kubeconfig")
	auditLog := fs.String("audit-log", "", "kind mode: host path of the API server audit log")
	converge := fs.Duration("converge-timeout", 90*time.Second, "time allowed to converge after each change")
	quiet := fs.Duration("quiet-window", 60*time.Second, "R3 steady-state window")
	writer := fs.Duration("writer-duration", 30*time.Second, "R6 concurrent-writer duration")
	usageFile := fs.String("usage-file", "", "JSON object of agent token usage (from runner/usage.py); costUSD is lifted into the result")
	_ = fs.Parse(args)
	if *spec == "" {
		return fmt.Errorf("--spec is required")
	}
	c, err := contract.Load(*spec)
	if err != nil {
		return err
	}
	fx, err := checks.ForSpec(c.Spec)
	if err != nil {
		return err
	}
	opDir, err := filepath.Abs(*op)
	if err != nil {
		return err
	}
	if *workdir == "" {
		if *workdir, err = os.MkdirTemp("", "gauntlet-"); err != nil {
			return err
		}
	}
	var logw io.Writer = os.Stderr
	if *logPath != "" {
		f, err := os.Create(*logPath)
		if err != nil {
			return err
		}
		defer f.Close()
		logw = f
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	run := &results.Run{
		Spec: c.Spec, Setup: *setup, Model: *model, Agent: *agent, Attempt: *attempt,
		StartedAt: time.Now(), Mode: *mode,
	}
	if *usageFile != "" {
		if b, err := os.ReadFile(*usageFile); err == nil {
			u := map[string]float64{}
			if json.Unmarshal(b, &u) == nil && len(u) > 0 {
				run.CostUSD = u["costUSD"]
				delete(u, "costUSD")
				run.Usage = u
			}
		}
	}
	switch *ownTests {
	case "true", "false":
		v := *ownTests == "true"
		run.OwnTestsPassed = &v
	}

	env, err := harness.Start(ctx, harness.Options{
		OperatorDir: opDir, Contract: c, Mode: harness.Mode(*mode), WorkDir: *workdir, Log: logw,
		KindKubeconfig: *kubeconfig, KindAuditLog: *auditLog,
	})
	if err != nil {
		run.BuildOK = false
		run.Notes = append(run.Notes, err.Error())
		run.FinishedAt = time.Now()
		printSummary(run, *visible)
		if *out != "" {
			_ = run.Save(*out)
		}
		return err
	}
	defer env.Stop()
	run.BuildOK = true
	run.Rules = checks.Run(ctx, env, fx, checks.Config{
		VisibleOnly: *visible, ConvergeTimeout: *converge, QuietWindow: *quiet, WriterDuration: *writer, Log: logw,
	})
	run.FinishedAt = time.Now()
	printSummary(run, *visible)
	if *out != "" {
		return run.Save(*out)
	}
	return nil
}

func printSummary(run *results.Run, visibleOnly bool) {
	fmt.Printf("\n%s  (%s)\n", run.Spec, run.Mode)
	if !run.BuildOK {
		fmt.Println("  BUILD/SETUP FAILED:", strings.Join(run.Notes, "; "))
		return
	}
	pass := 0
	for _, r := range run.Rules {
		status := "PASS"
		switch {
		case r.Skipped:
			status = "SKIP"
		case !r.Pass:
			status = "FAIL"
		default:
			pass++
		}
		fmt.Printf("  %s  %s\n", status, r.ID)
		for _, d := range r.Details {
			fmt.Printf("        %s\n", d)
		}
	}
	fmt.Printf("  %d/%d rules passed", pass, len(run.Rules))
	if visibleOnly {
		fmt.Print(" (visible rules only)")
	}
	fmt.Println()
}

func cmdRBAC(args []string) error {
	fs := flag.NewFlagSet("rbac", flag.ExitOnError)
	spec := fs.String("spec", "", "spec directory")
	op := fs.String("operator", ".", "operator project directory")
	_ = fs.Parse(args)
	c, err := contract.Load(*spec)
	if err != nil {
		return err
	}
	roles, err := harness.ParseRoleFile(filepath.Join(*op, "config", "rbac", "role.yaml"))
	if err != nil {
		return err
	}
	var chk []rbaccheck.Role
	for _, r := range roles {
		chk = append(chk, r.ForCheck())
	}
	findings := rbaccheck.Check(chk, c)
	if len(findings) == 0 {
		fmt.Println("R7 static: PASS")
		return nil
	}
	fmt.Println("R7 static: FAIL")
	for _, f := range findings {
		fmt.Println("  -", f)
	}
	return nil
}

func cmdReport(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("give result files or globs, e.g. results/*.json")
	}
	runs, err := results.LoadAll(args...)
	if err != nil {
		return err
	}
	fmt.Print(results.Markdown(runs, contract.AllRules, contract.HiddenRules))
	return nil
}
