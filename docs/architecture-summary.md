# Architecture Summary

Pulse is a Kubernetes operator for HTTP, journey, MCP-over-HTTP, and gRPC health canaries. It separates desired-state reconciliation from probe execution and keeps model work off the controller's reconcile path.

## Components

- **Controller manager** watches `HttpCanary`, `GrpcCanary`, and `AnomalyPolicy` resources. It renders probe/auth configuration, reconciles shared runtime resources, and projects results and incidents into CR status.
- **Probe runner StatefulSet** executes deterministic protocol checks. Stable ordinals shard probes by name when replicas exceed one. It also performs opt-in Potion body embeddings and local EWMA latency detection; response bodies stay inside the runner.
- **Incident engine Deployment** exists only when intelligence is used. It aggregates runner snapshots, merges failures using declared topology or sufficient same-space MiniLM similarity, ranks a root from graph/onset evidence, clusters novelty, and executes policy actions.
- **Services and ConfigMaps/Secrets** provide discovery and configuration. `pulse-probe-runner` is the status endpoint for a single replica; sharded status collection addresses each StatefulSet pod. `pulse-incident-engine` exposes the cluster-wide results and incident views.

## Data Flow

1. A user applies a canary and, optionally, an `AnomalyPolicy`.
2. The controller renders the full desired probe set and reconciles the StatefulSet, Services, configuration, and optional incident engine.
3. Each runner owns a stable shard, executes checks, and retains only its latest results. Intelligence signals—not raw bodies—are pushed to the engine.
4. The engine aggregates all shards. Deterministic failures may become correlated incidents; passing checks can still raise body-drift or latency signals.
5. The status syncer reads the complete live result view and open incidents, updating CR status only when its meaningful fields change.

An engine restart loses in-memory open incidents, novelty clusters, learned topology proposals, and aggregated history. Runner restarts lose drift/latency baselines. Neither component currently provides durable HA state.

For a runnable, evidence-driven tour, use [the Kind quick start](quick-start.html). For detail, continue with [architecture.md](architecture.md), [reconciliation-design.md](reconciliation-design.md), [scaling.md](scaling.md), and [operations.md](operations.md).
