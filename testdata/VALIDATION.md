# Suite validation

Run on 23 September 2026 in envtest (Kubernetes v1.34.1 API server), with the
short timings from `testdata/validate.sh` (`--converge-timeout 30–40s
--quiet-window 15–20s --writer-duration 10–15s`). Re-run with
`testdata/validate.sh` after any change to `pkg/checks` or `pkg/harness`.

## Reference operators: every rule passes

| Operator | R1 | R2 | R3 | R4 | R5 | R6 | R7 | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| webapp-reference | pass | pass | pass | pass | pass | pass | pass | 0 writes/min idle; 225 concurrent writes, 1 conflict handled |
| configsync-reference | pass | pass | pass | pass | pass | pass | pass | crash after the first ConfigMap copy |
| manageddatabase-reference | pass | pass | pass | pass | pass | pass | pass | crash at cloud create, recovered by lookup plus password reset: 1 cloud create call |

R5 is checked statically (owner references) in envtest. Garbage collection
itself needs kind mode.

## WebApp mutants: each is caught by the rule it targets

| Mutant | What it breaks | R1 | R2 | R3 | R4 | R5 | R6 | R7 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| R1 | Creates the Service only in the reconcile that created the Deployment | **FAIL** | n/e | n/e | pass | n/e | n/e | pass |
| R2 | Sets `observedGeneration` once, never updates it | pass | **FAIL** | n/e | pass | pass | fail | pass |
| R3 | Rewrites status on every reconcile, requeues every second | pass | pass | **FAIL** (60 writes/min) | pass | pass | pass | pass |
| R4 | Adds a finalizer it never removes | pass | pass | pass | **FAIL** | pass | pass | pass |
| R5 | No owner references on children | pass | fail | n/e | pass | **FAIL** | fail | pass |
| R6 | Replaces the Deployment's labels | pass | pass | pass | pass | pass | **FAIL** | pass |
| R7 | `+kubebuilder:rbac:groups=*,resources=*,verbs=*` | pass | pass | pass | pass | pass | pass | **FAIL** |

n/e = not evaluated, because an earlier phase never converged (reported as a fail).

The extra failures are real consequences, not false positives. Without owner
references, `Owns()` never maps Deployment events back to the WebApp, so status
goes stale (R5 → R2, R6). A stale `observedGeneration` means the operator never
looks converged after a change (R2 → R6).
