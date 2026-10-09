---
name: controller-best-practices
description: Rules for writing correct Kubernetes controllers and operators with Kubebuilder and controller-runtime. Use when implementing or reviewing a Reconcile loop, finalizers, status updates, owner references or RBAC markers.
---

# Writing a Kubernetes controller that survives production

Follow these rules when you implement a reconciler. Each one prevents a failure
that shows up only under crashes, concurrency or scale.

## 1. Reconcile is level-triggered and idempotent

- Compute the desired state from the current spec and the observed world every
  time. Don't rely on what the previous reconcile did, and don't use in-memory
  state across reconciles.
- Assume the process can be killed between any two lines. Before creating an
  external resource, look it up by a **deterministic name** derived from the
  object (for example `<name>-<namespace>` or the object's UID). If a create
  returns "already exists", adopt the existing resource instead of failing.
- Record external IDs in `status` as soon as you have them.
- If a secret or credential is only returned once (for example on create), and
  you crashed before storing it, recover by rotating it through the API rather
  than creating a second resource.

## 2. Status discipline

- Enable the status subresource (`+kubebuilder:subresource:status`) and write
  status only with `r.Status().Update` or `r.Status().Patch`.
- Set `status.observedGeneration = metadata.generation` for the generation you
  just reconciled.
- Use `meta.SetStatusCondition` for conditions. It only changes
  `lastTransitionTime` when the status value actually changes.
- **Only write status when it changed.** Compare the new status with the old
  one and skip the write if they're equal.

## 3. Don't write when nothing changed

- For children, use `controllerutil.CreateOrUpdate` or `CreateOrPatch` with a
  mutate function that sets only the fields you own. It skips the update when
  nothing changed.
- Don't requeue on a timer when you're converged. Watch what you depend on
  (`Owns(...)`, `Watches(...)`) and let events trigger reconciles. Use
  `RequeueAfter` only while waiting on something external, such as a cloud
  resource that is still provisioning.
- Don't update the object just to add an annotation or timestamp on every loop.

## 4. Finalizers

- Add a finalizer only if you must clean up something Kubernetes can't, such
  as an external resource. Don't add one just for in-cluster children; owner
  references handle those.
- On deletion (`DeletionTimestamp` set): clean up externally, wait until the
  external resource is really gone (or the policy says to keep it), then remove
  the finalizer. Never block forever. Handle "not found" as success.
- Handle deletion that arrives while a create is still in flight: look up by
  deterministic name, and delete what you find.

## 5. Ownership and garbage collection

- Set a controller owner reference on every child with
  `controllerutil.SetControllerReference`.
- A namespaced owner can only own objects in its own namespace. A
  cluster-scoped owner can own namespaced objects.
- Declare `Owns(&corev1.Secret{})` (etc.) in `SetupWithManager`, so changes to
  children trigger reconciles.

## 6. Conflicts and fields you don't own

- `409 Conflict` is normal. Return the error (controller-runtime requeues with
  backoff) or retry with `retry.RetryOnConflict`. Never exit the process or
  panic on it.
- Other controllers and humans add labels and annotations to your children.
  Merge your labels into the existing map instead of replacing it. Prefer patches
  or `CreateOrUpdate` mutate functions over overwriting whole objects.
- Re-read with `Get` before updating. Don't update from a stale copy.

## 7. Least-privilege RBAC

- Write `+kubebuilder:rbac` markers for exactly the groups, resources and verbs
  you use. Never use `*`.
- Include `<resource>/status` and `<resource>/finalizers` for your own type.
- Don't request Secrets, Namespaces or cluster-wide access unless the spec needs it.
- Run `make manifests` after changing markers.

## Before you finish

- `make manifests generate` succeeds, and `config/rbac/role.yaml` has no wildcards.
- `go build -o bin/manager ./cmd` succeeds.
- `make test` passes.
