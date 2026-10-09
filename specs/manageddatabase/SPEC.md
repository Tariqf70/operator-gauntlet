# Spec: ManagedDatabase operator

Build a Kubernetes operator in Go with Kubebuilder (v4 layout) that manages
databases in an external cloud API.

## API

- Group/version: `bench.example.com/v1alpha1` (Kubebuilder domain `example.com`, group `bench`)
- Kind: `ManagedDatabase`, namespaced, plural `manageddatabases`

```yaml
apiVersion: bench.example.com/v1alpha1
kind: ManagedDatabase
metadata:
  name: orders
  namespace: shop
spec:
  engine: postgres        # enum: postgres | mysql. Immutable after creation.
  sizeGB: 20              # integer, 10..1000
  deletionPolicy: Delete  # enum: Delete | Retain. Default Delete.
status:
  databaseID: db-7f3a...  # ID returned by the cloud API
  endpoint: orders-shop.db.fakecloud.local:5432
  phase: Creating         # Creating | Available | Deleting | Failed
  observedGeneration: 3
  conditions:             # standard metav1.Condition list
  - type: Ready           # True when the cloud database is "available" and the Secret exists
    status: "True"
```

## Behavior (acceptance contract)

1. For each ManagedDatabase, exactly one database exists in the cloud API, even
   if the operator is restarted at any moment.
2. The operator creates a Secret named `<name>-conn` in the same namespace, with
   keys `endpoint`, `username` and `password`. The password must be valid for
   the cloud database.
3. When `spec.sizeGB` changes, the cloud database is resized.
4. When the ManagedDatabase is deleted:
   - with `deletionPolicy: Delete`, the cloud database is deleted before the
     ManagedDatabase object goes away;
   - with `deletionPolicy: Retain`, the cloud database is left in place.
   Deletion must complete within 30 seconds of the cloud API's response.
5. The Secret must be cleaned up by Kubernetes garbage collection when the
   ManagedDatabase is deleted.
6. `status` reflects the current spec and the cloud state. `Ready` is `True`
   only when the database is `available` and the Secret exists.

## Environment

- The cloud API base URL is in the environment variable `FAKECLOUD_URL`.
  The API is documented in `CLOUD_API.md`.
- The manager is started with `--leader-elect=false`, `--metrics-bind-address=0`
  and `--health-probe-bind-address=0`, and the kubeconfig in `KUBECONFIG`.
- The manager binary is built with `go build -o bin/manager ./cmd`.
