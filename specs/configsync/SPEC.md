# Spec: ConfigSync operator

Build a Kubernetes operator in Go with Kubebuilder (v4 layout) that copies a
ConfigMap into every namespace that matches a label selector.

## API

- Group/version: `bench.example.com/v1alpha1` (Kubebuilder domain `example.com`, group `bench`)
- Kind: `ConfigSync`, **cluster-scoped**, plural `configsyncs`

```yaml
apiVersion: bench.example.com/v1alpha1
kind: ConfigSync
metadata:
  name: shared-ca
spec:
  source:
    namespace: platform
    name: ca-bundle
  namespaceSelector:          # metav1.LabelSelector
    matchLabels:
      team-tier: prod
status:
  syncedNamespaces: ["payments", "search"]
  observedGeneration: 2
  conditions:
  - type: Ready               # True when every matching namespace holds an up-to-date copy
    status: "True"
```

## Behavior (acceptance contract)

1. Every namespace that matches `namespaceSelector` (the source's own namespace
   excepted) holds a ConfigMap with the source's name, `data` and `binaryData`,
   labeled `bench.example.com/managed-by: <configsync-name>`.
2. Changes to the source ConfigMap reach every copy within 10 seconds.
3. When a namespace stops matching, or a new namespace starts matching, copies
   are removed or added within 10 seconds.
4. Copies are cleaned up by Kubernetes garbage collection when the ConfigSync is
   deleted, even if the operator isn't running.
5. If the source doesn't exist, `Ready` is `False` with reason `SourceNotFound`,
   and existing copies are left untouched.
6. `status` reflects the current spec. `status.syncedNamespaces` is sorted.

## Environment

- The manager is started with `--leader-elect=false`, `--metrics-bind-address=0`
  and `--health-probe-bind-address=0`, and the kubeconfig in `KUBECONFIG`.
- The manager binary is built with `go build -o bin/manager ./cmd`.
