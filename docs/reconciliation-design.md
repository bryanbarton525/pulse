# Reconciliation Design

This document describes how Pulse converges desired state for canary execution without tying operational polling to controller reconciliation.

## Goals

- Keep reconciliation idempotent
- Collapse many canary events into one infrastructure update
- Avoid performing network probes inside the controller
- Keep status writes separate from infrastructure writes

## Reconcile Trigger Model

`HttpCanaryReconciler.SetupWithManager` maps all `HttpCanary` events to one fixed request key instead of reconciling each object independently.

That means:

- 1 change still causes a full infrastructure pass
- 1,000 changes in a burst still cause one deduplicated queue item
- The reconciler always treats the list of all canaries as source of truth

This is the main reason the controller scales more cleanly than a per-resource reconcile design.

## Desired State Generation

Each reconcile pass:

1. Lists every `HttpCanary`
2. Converts them into the probe runner config model
3. Marshals the config to YAML
4. Writes the YAML into the shared ConfigMap

The generated probe key is `namespace/name`, which gives the status syncer a stable mapping between runner output and CR status.

## Managed Resources

The reconciler owns three resources in the operator namespace:

- `ConfigMap/pulse-probe-config`
- `Deployment/pulse-probe-runner`
- `Service/pulse-probe-runner`

`controllerutil.CreateOrUpdate` is used for all three so reconciliation stays declarative and repeatable.

## What Reconcile Does Not Do

The reconciler intentionally does not:

- poll target URLs
- update `HttpCanary.status`
- use `RequeueAfter` for periodic work

Those choices keep the controller responsive and reduce unnecessary writes to the API server.

## Failure Modes

### Config generation failure

If listing canaries or marshaling probe config fails, reconcile exits with an error and is retried by controller-runtime.

### Runner infrastructure drift

If the Deployment or Service is deleted or mutated, the next canary event causes the controller to restore the expected shape.

### No canaries present

The controller still reconciles the shared infrastructure. This keeps the runner ready for future resources and ensures the config stays accurate even when the last canary is removed.

## Current Constraints

- The controller assumes one shared runner Deployment.
- Config delivery depends on ConfigMap size limits.
- The controller creates infrastructure in one namespace only.
- Cross-namespace owner references are not used.

## Likely Next Design Steps

- Shard runner workloads when probe count becomes large
- Add readiness semantics around runner availability before status sync begins reporting failures
- Introduce stronger rollout controls for runner image versioning and configuration changes
