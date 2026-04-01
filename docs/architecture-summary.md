# Architecture Summary

Pulse is a Kubernetes operator for endpoint canaries built around a split control-plane and data-plane model.

## Core Model

- The controller watches `HttpCanary` custom resources cluster-wide.
- The controller does not execute probes directly.
- Instead, it renders the desired probe set into a ConfigMap and ensures a probe runner Deployment and Service exist in the operator namespace.
- A background status sync loop polls the probe runner's `/results` endpoint and writes the latest state back to each `HttpCanary` status.

## Components

### Controller manager

The manager runs inside `cmd/main.go` and hosts two long-running components:

- `HttpCanaryReconciler` to manage shared infrastructure
- `StatusSyncer` to project probe results back into CR status

### Reconciler

The reconciler in `internal/controller/httpcanary_controller.go` handles desired state for the shared runtime:

- Lists every `HttpCanary`
- Builds a single probe configuration payload
- Reconciles `pulse-probe-config` ConfigMap
- Reconciles `pulse-probe-runner` Deployment
- Reconciles `pulse-probe-runner` Service

All canary events map to one fixed reconcile key, so a burst of resource changes still collapses to one reconcile pass.

### Probe runner

The probe runner in `cmd/proberunner/main.go` and `internal/proberunner/` is the execution plane:

- Reads the generated config file
- Schedules HTTP checks on each configured interval
- Stores the latest result in memory
- Exposes `/results`, `/metrics`, and `/healthz`

### Status syncer

The status syncer in `internal/controller/status_syncer.go` runs on a fixed interval and:

- Calls the runner Service
- Lists all `HttpCanary` resources
- Updates only changed status fields

This keeps reconciliation focused on infrastructure convergence and avoids timer-driven reconcile storms.

## Data Flow

1. A user applies an `HttpCanary` object.
2. The controller lists all canaries and writes the current desired probe set to the ConfigMap.
3. The probe runner loads the config and executes checks on schedule.
4. The status syncer polls `/results` and updates `.status` on matching resources.
5. Users observe health through `kubectl get httpcanaries` or `kubectl get -o yaml`.

## Deployment Model

- `HttpCanary` resources can live in any namespace.
- Shared infrastructure runs in the operator namespace, defaulting to `pulse-system`.
- The controller needs cluster-scoped read access to `HttpCanary` resources and namespaced write access for the runner infrastructure.

## Key Tradeoffs

- The current design favors simple shared infrastructure over per-canary isolation.
- A single runner keeps the control flow easy to reason about, but it becomes the main scaling boundary.
- ConfigMap-based delivery is native and simple, but large fleets may eventually need sharding.

For full design detail, continue with [architecture.md](architecture.md), [reconciliation-design.md](reconciliation-design.md), [scaling.md](scaling.md), and [testing-and-validation.md](testing-and-validation.md).
