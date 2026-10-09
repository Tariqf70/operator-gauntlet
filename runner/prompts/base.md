You are building a Kubernetes operator in Go with Kubebuilder (v4 project layout).
The full specification is in `SPEC.md`{{CLOUD_API_NOTE}}. Work only in the current directory.

You are done when all of these are true:

- `make manifests generate` has been run, so `config/crd/bases/` and `config/rbac/role.yaml` are current.
- `go build -o bin/manager ./cmd` succeeds.
- `make test` passes.

Don't ask questions. Make reasonable decisions and finish the task.
