# operator-gauntlet

**Do AI coding agents write Kubernetes operators that survive production?**

operator-gauntlet is a fault-injection test suite for Kubernetes operators. It
checks an operator against seven rules that experienced operator authors
usually learn the hard way: crash safety, status discipline, quiet steady
state, finalizers, ownership, conflict handling and least-privilege RBAC. It
then uses those rules to compare AI coding agents across four setups, from
"spec only" to "spec, scaffold, best-practice skill and test feedback".

The headline question: **how many agent-built operators that pass their own
tests still break at least one rule?**

## Why this matters

Ask an agent for an operator and you get a project that compiles, passes
`make test` and reconciles the happy path. The bugs that matter show up later:

- A crash between "create" and "record that I created it" leaves duplicate
  cloud resources.
- A finalizer that is never removed leaves namespaces stuck in `Terminating`.
- A status update on every loop slowly fills etcd.
- A `+kubebuilder:rbac` marker with wildcards gives the operator cluster-admin.

Unit tests and LLM judges rarely catch these failures. operator-gauntlet
reproduces each one on purpose and checks the result.

## How it works

```
             ┌──────────────── envtest or kind control plane ────────────────┐
 operator ──▶│  API proxy  ──▶  kube-apiserver  ──▶  audit log               │
 (own RBAC   │  SIGKILL at the        ▲                (writes/min, 403s,    │
  user only) │  first create          │                 409s, subresources)  │
             │                 concurrent writer                             │
             │                 (forces 409 conflicts)                        │
             └───────────────────────────────────────────────────────────────┘
                        fake cloud API (crash hook at stored create)
```

- **Real RBAC.** Each operator runs as a user bound only to the RBAC it
  generated. Missing permissions show up as 403s, not silent passes.
- **Deterministic crashes.** An API proxy SIGKILLs the manager the instant its
  first create succeeds, before it sees the response. For the ManagedDatabase
  spec, the fake cloud kills it the instant it stores a create.
- **Forced conflicts.** A concurrent writer bumps `resourceVersion` on the
  custom resource and its children at about 5 Hz while spec changes land.
- **Audit-log evidence.** The suite counts the operator's API writes while it
  is idle and checks that status goes through the status subresource.
- **No LLM judge.** Every rule is a deterministic pass/fail check.

## The seven rules

Full detail is in [RULES.md](RULES.md).

| ID | Rule | Hidden in S4 |
| --- | --- | --- |
| R1 | Crash-safe, idempotent reconcile | |
| R2 | Status discipline (`observedGeneration`, no idle flapping) | |
| R3 | Bounded steady-state writes | **Yes** |
| R4 | Finalizer lifecycle: deletion never hangs or leaks | |
| R5 | Ownership and garbage collection | |
| R6 | Conflict handling, no lost updates | **Yes** |
| R7 | Least-privilege RBAC | |

Each spec's contract marks every rule as *stated* (the spec tells the agent) or
*implicit* (an expert would follow it anyway). Results report the two
separately.

## The experiment

Three operator specs, each with a different main risk:

| Spec | What it builds | Main risk |
| --- | --- | --- |
| [WebApp](specs/webapp/SPEC.md) | Deployment and Service from one custom resource | Ownership, status, conflicts |
| [ConfigSync](specs/configsync/SPEC.md) | Copies a ConfigMap into every namespace that matches a selector | Fan-out, steady-state writes |
| [ManagedDatabase](specs/manageddatabase/SPEC.md) | Databases in an external cloud API | Crash safety, finalizers, leaks |

Four setups, each adding one thing:

| Setup | What the agent gets |
| --- | --- |
| S1 | The spec only. The agent runs `kubebuilder init` itself. |
| S2 | A pre-scaffolded project with the API types written |
| S3 | S2 plus an Agent Skill of controller best practices ([skill/](skill/controller-best-practices/SKILL.md)) |
| S4 | S3 plus `./gauntlet-check`, which runs the **visible** rules only |

R3 and R6 stay hidden in S4. If hidden-rule pass rates rise with feedback, the
agent learned the rules. If only the visible rules improve, it learned to pass
the tests.

Agent adapters exist for Codex CLI, Claude Code and Gemini CLI. Each run
records the agent's token usage, its own `make test` result and the full
gauntlet log.

## Is the suite itself trustworthy?

A test suite is only useful if it catches real bugs and passes correct code.
Validation ([testdata/VALIDATION.md](testdata/VALIDATION.md)):

- **Reference operators pass 7/7** for all three specs.
- **Seven WebApp mutants, one per rule, each fail the rule they target.** For
  example, the R3 mutant rewrites status every reconcile and is caught at 60
  writes/min, and the R7 mutant uses `groups=*,resources=*,verbs=*`.
- When a mutant also fails another rule, the extra failure is a real
  consequence. For example, missing owner references mean `Owns()` never
  triggers, so status goes stale.

## Status

| Part | State |
| --- | --- |
| Harness, checks, CLI | Done. Runs against real envtest (Kubernetes v1.34.1). |
| Reference operators (3 specs) | Done. Each passes 7/7. |
| Mutants (one per rule) | Done. Each is caught by its rule. |
| Runner (prepare, agent, own tests, score, report) | Done. Tested end to end with a stand-in "oracle" agent (7/7). |
| Agent adapters (Codex, Claude Code, Gemini) | Written. **Not yet run with real agents.** |
| Kind mode (garbage collection, APF throttling) | Written. **Not yet run.** |

## Quick start

You need Go, and for the pilot at least one agent CLI (`codex`, `claude` or
`gemini`). `make setup` installs everything else into `./bin`. It uses Homebrew
only for GNU `timeout`.

```bash
make setup      # kubebuilder, envtest binaries, GNU timeout; builds bin/gauntlet
make selftest   # scores the WebApp reference operator: expect 7/7 in about a minute
make oracle     # runs the full pilot pipeline with a stand-in agent: expect 7/7
```

Score one operator of your own (a full-length run takes 3–5 minutes):

```bash
source .env.local
bin/gauntlet run --spec specs/webapp --operator <dir>
```

## Running the pilot

> **Warning:** The agents run shell commands unattended. Run the pilot inside a
> disposable VM or container (for example colima or a devcontainer). The
> Claude Code and Gemini adapters refuse to run unless `GAUNTLET_SANDBOXED=1`.
> Codex uses its own workspace-write sandbox.

1. List three models with exact versions in `runner/models.txt` (see
   `runner/models.example.txt`).
2. Run the pilot. It takes hours. Finished runs are skipped, so you can stop
   and resume at any time.
   ```bash
   make pilot
   ```
3. Produce the report. It is also written to `results/REPORT.md`.
   ```bash
   make report
   ```

Each run gets a fresh workspace, `out/<spec>-<setup>-<model>-<n>/`, with the
agent's output, its token usage (`.gauntlet/usage.json`), its `make test` log
and the gauntlet log.

`runner/run-pilot.sh` reads these environment variables:

| Variable | Effect |
| --- | --- |
| `SPECS`, `SETUPS`, `RUNS` | Which slice of the matrix to run |
| `AGENT_TIMEOUT` | Time limit per agent run (default 45m) |
| `GAUNTLET_FLAGS` | Extra flags for `gauntlet run` (keep empty for real runs) |
| `SKIP_OWN_TESTS=1` | Skip `make test` (quick checks only) |
| `MODELS_FILE`, `OUT`, `RESULTS` | Paths |

Codex needs network access for `go mod download`. If builds fail with network
errors, allow network for workspace-write in `~/.codex/config.toml`.

## Kind mode

envtest has no kube-controller-manager, so garbage collection cannot run there.
In envtest, R5 checks owner references statically. Kind mode checks real
garbage collection and adds API Priority and Fairness throttling:

```bash
kind/up.sh
bin/gauntlet run --mode kind --kubeconfig .kind-kubeconfig --audit-log .kind-audit/audit.log \
  --spec specs/webapp --operator testdata/webapp-reference
```

## Changing the suite

After any change to `pkg/`, run the validation (about 10 minutes). It scores
the 3 references and 7 mutants and prints a pass/fail matrix:

```bash
make validate
```

The references must pass every rule, and each mutant must fail its rule.

## Layout

```
specs/<spec>/SPEC.md         what the agent reads (+ CLOUD_API.md for ManagedDatabase)
specs/<spec>/contract.json   stated/implicit rules, RBAC allow-list, crash point
specs/<spec>/scaffold/       API types for S2–S4
RULES.md                     the seven rules and how each is checked
pkg/harness                  envtest/kind control plane, scoped RBAC user, API proxy, concurrent writer
pkg/checks                   per-spec fixtures and the R1–R7 phases
pkg/fakecloud                fake cloud database API with a crash hook
pkg/audit                    audit-log parsing: writes/min, status subresource use, 403s, 409s
pkg/procctl                  build, run, SIGKILL and restart the manager binary
pkg/rbaccheck                least-privilege check against the contract
pkg/results                  result files and the "k of n" report
cmd/gauntlet                 CLI: run | rbac | report
cmd/fakecloud                standalone fake cloud for local development
runner/                      pilot matrix, prompts, agent adapters, usage extraction
skill/                       the Agent Skill used in S3 and S4
testdata/                    reference operators, mutant generator, validation script and results
kind/                        kind cluster with audit logging and APF throttling
```

## Known limitation

The harness passes `--advertise-address=127.0.0.1` to kube-apiserver, so
envtest starts in sandboxes with no default route. Kubebuilder's own
`make test` does not, so a generated project's tests can fail in such
sandboxes even when the code is correct. Run the pilot on a machine with a
normal network.

## License

Apache License 2.0. See [LICENSE](LICENSE).
