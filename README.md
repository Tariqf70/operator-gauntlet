# operator-gauntlet

Fault-tests Kubernetes operators against the rules experienced operator authors
follow, and compares how well AI coding agents write them under four setups.

Each generated operator runs as a user bound only to its own RBAC, behind an API
proxy. The proxy SIGKILLs it at a deterministic crash point (the instant its
first create succeeds, or the instant the fake cloud stores a create). A
concurrent writer forces 409 conflicts, and the audit log counts the
operator's API writes while idle. Every rule ([RULES.md](RULES.md)) is a
deterministic pass/fail check with no LLM judge.

## Status

| Part | State |
| --- | --- |
| Harness, checks, CLI (`go vet ./...` clean) | Done. Compiled and run against real envtest (Kubernetes v1.34.1). |
| Reference operators for all three specs | Done: each passes 7/7 ([testdata/VALIDATION.md](testdata/VALIDATION.md)) |
| Seven WebApp mutants, one per rule | Done: each is caught by the rule it targets |
| Runner (prepare → agent → own tests → score → report) | Done. Tested end to end with the `replay-reference` oracle adapter (7/7) and the S4 `./gauntlet-check` (visible rules only). |
| Agent adapters | Codex CLI, Claude Code, Gemini CLI, written from their docs. **Not yet run with real agents.** |
| Kind mode (garbage collection, APF throttling) | Written, **not yet run** (needs Docker/colima) |

## Run it on your Mac

You need Go and at least one agent CLI (`codex`, `claude` or `gemini`). `make setup` installs everything else into `./bin`, using Homebrew only for GNU `timeout`.

```bash
make setup      # kubebuilder, envtest binaries, GNU timeout; builds bin/gauntlet; writes .env.local and runner/models.txt
make selftest   # scores the WebApp reference operator: expect 7/7 in about a minute
make oracle     # runs the whole pilot pipeline with a stand-in agent: expect 7/7
$EDITOR runner/models.txt   # setup lists the agent CLIs it found; pin real model IDs
make pilot      # the real pilot (hours; stop and rerun any time, finished runs are skipped)
make report     # the "k of n" table; also written to results/REPORT.md
```

- **Codex** runs in its own workspace-write sandbox. Agents need network
  access for `go mod download`. If builds fail with network errors inside the
  sandbox, allow network for workspace-write in `~/.codex/config.toml`.
- **Claude Code and Gemini** run with approvals off. Their adapters refuse to
  run unless `GAUNTLET_SANDBOXED=1`, so run those inside a VM or container.
- A single operator: `source .env.local && bin/gauntlet run --spec specs/webapp --operator <dir>`.
  A full-length run takes 3–5 minutes.

## Validate the suite (after any change to `pkg/`)

```bash
make validate      # ~10 min: 3 references + 7 mutants, prints a pass/fail matrix
```

The references must pass everything, and each mutant must fail its rule. See
[testdata/VALIDATION.md](testdata/VALIDATION.md) for the last run and why some
mutants also fail other rules.

## Running the pilot

1. **Install** `kubebuilder` (v4) and GNU `timeout` (`brew install coreutils`).
2. **Choose models.** Copy `runner/models.example.txt` to `runner/models.txt`
   and list 3 models with exact versions.
3. **Sandbox the agents.** Run the pilot inside a disposable VM or container
   (colima or a devcontainer). The agents run shell commands unattended.
   `claude-code.sh` and `gemini.sh` refuse to run unless `GAUNTLET_SANDBOXED=1`.
   Codex uses its own workspace-write sandbox.
4. **Check the pipeline** with the oracle adapter. It should score 7/7:
   ```bash
   echo "oracle|replay-reference.sh|reference" > /tmp/oracle.txt
   MODELS_FILE=/tmp/oracle.txt SPECS=webapp SETUPS=S3 RUNS=1 OUT=/tmp/o RESULTS=/tmp/r runner/run-pilot.sh
   ```
5. **Run the pilot**, then produce the report:
   ```bash
   SPECS="configsync manageddatabase webapp" SETUPS="S1 S2 S3 S4" RUNS=2 runner/run-pilot.sh
   bin/gauntlet report results/*.json     # also written to results/REPORT.md
   ```

Each run gets a fresh workspace, `out/<spec>-<setup>-<model>-<n>/`, with the
agent's output, its token usage (`.gauntlet/usage.json`), its own `make test`
log and the gauntlet log. Runs that are already scored are skipped, so you can
stop and resume. The report prints the abstract's headline: how many builds
that passed their own tests broke at least one rule.

| Setup | What the agent gets |
| --- | --- |
| S1 | The spec only. The agent runs `kubebuilder init` itself. |
| S2 | A pre-scaffolded project with the API types written (`specs/*/scaffold/types.go.tmpl`) |
| S3 | S2 plus the Agent Skill in `skills/controller-best-practices/SKILL.md` |
| S4 | S3 plus `./gauntlet-check`, which runs the **visible** rules only. R3 and R6 stay hidden. |

`runner/run-pilot.sh` also reads these environment variables:

| Variable | Effect |
| --- | --- |
| `SPECS`, `SETUPS`, `RUNS` | Which slice of the matrix to run |
| `AGENT_TIMEOUT` | Time limit per agent run (default 45m) |
| `GAUNTLET_FLAGS` | Extra flags for `gauntlet run` (keep empty for real runs) |
| `SKIP_OWN_TESTS=1` | Skip `make test` (for quick checks only) |
| `MODELS_FILE`, `OUT`, `RESULTS` | Paths |

## Kind mode (R5 garbage collection, APF throttling)

envtest has no kube-controller-manager, so garbage collection can't run there.
R5 checks owner references statically in envtest, and for real on kind:

```bash
kind/up.sh
bin/gauntlet run --mode kind --kubeconfig .kind-kubeconfig --audit-log .kind-audit/audit.log \
  --spec specs/webapp --operator testdata/webapp-reference
```

## Layout

```
specs/<spec>/SPEC.md         what the agent reads (+ CLOUD_API.md for ManagedDatabase)
specs/<spec>/contract.json   stated/implicit rules, RBAC allow-list, crash point
specs/<spec>/scaffold/       API types for S2–S4
RULES.md                     the seven rules and how each is checked
pkg/harness                  envtest/kind control plane, scoped RBAC user, API proxy, concurrent writer
pkg/checks                   per-spec fixtures and the R1–R7 phases
pkg/fakecloud                fake cloud DB API with a crash hook (ManagedDatabase)
pkg/audit                    audit-log parsing: writes/min, status-subresource use, 403s, 409s
pkg/procctl                  build/run/SIGKILL/restart the manager binary
pkg/rbaccheck                least-privilege check against the contract
pkg/results                  result files and the "k of n" report
cmd/gauntlet                 CLI: run | rbac | report
cmd/fakecloud                standalone fake cloud for local development
runner/                      pilot matrix, prompts, agent adapters, usage extraction
skill/                       the Agent Skill used in S3/S4
testdata/                    reference operators, mutant generator, validation script and results
kind/                        kind cluster with audit logging and APF throttling
```

## Notes

- The harness passes `--advertise-address=127.0.0.1` to kube-apiserver, so
  envtest starts in sandboxes with no default route. Kubebuilder's own
  `make test` doesn't, so a generated project's tests can fail in such
  sandboxes even when the code is fine. Run the pilot on a machine with a normal
  network.
- Rename the module path in `go.mod` from `example.com/operator-gauntlet` to
  your repository path, and choose Apache-2.0 when you create the repository.
