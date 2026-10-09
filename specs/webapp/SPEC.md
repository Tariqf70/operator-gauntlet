# Spec: WebApp operator

Build a Kubernetes operator in Go with Kubebuilder (v4 layout) that runs a
stateless web application from a single custom resource.

## API

- Group/version: `bench.example.com/v1alpha1` (Kubebuilder domain `example.com`, group `bench`)
- Kind: `WebApp`, namespaced, plural `webapps`

```yaml
apiVersion: bench.example.com/v1alpha1
kind: WebApp
metadata:
  name: storefront
  namespace: shop
spec:
  image: ghcr.io/example/storefront:1.4.2
  replicas: 3          # 0..50
  port: 8080           # container port, also the Service port
status:
  availableReplicas: 3
  url: http://storefront.shop.svc:8080
  observedGeneration: 5
  conditions:
  - type: Ready        # True when availableReplicas == spec.replicas
    status: "True"
  - type: Progressing  # True while a rollout is in progress
    status: "False"
```

## Behavior (acceptance contract)

1. The operator maintains a Deployment and a Service named after the WebApp, in
   the same namespace, labeled `app.kubernetes.io/name: <webapp-name>` and
   `app.kubernetes.io/managed-by: webapp-operator`.
2. Changes to `image`, `replicas` or `port` reach the Deployment and Service
   within 10 seconds.
3. Deployment and Service are cleaned up by Kubernetes garbage collection when
   the WebApp is deleted.
4. `status` reflects the current spec and the Deployment's status. `Ready` is
   `True` only when `availableReplicas == spec.replicas`.
5. Other tools in the cluster add their own labels and annotations to the
   Deployment and Service. Those must be preserved.

## Environment

- The manager is started with `--leader-elect=false`, `--metrics-bind-address=0`
  and `--health-probe-bind-address=0`, and the kubeconfig in `KUBECONFIG`.
- The manager binary is built with `go build -o bin/manager ./cmd`.
