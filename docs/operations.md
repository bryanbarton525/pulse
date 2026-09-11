# Operations Guide

This document covers how to run, inspect, and troubleshoot Pulse on a real cluster.

## Namespaces and Resources

- The operator defaults to `pulse-system` for shared infrastructure.
- `HttpCanary` resources can be created in any namespace.
- The controller creates these runtime resources in the operator namespace:
  - `ConfigMap/pulse-probe-config`
  - `Secret/pulse-probe-auth` when probes reference credentials
  - `StatefulSet/pulse-probe-runner`
  - `Service/pulse-probe-runner` and headless `Service/pulse-probe-runner-headless`
  - optional `Deployment/pulse-incident-engine` and `Service/pulse-incident-engine`

## Local Controller Against a Cluster

The repository supports `make run`, which runs the controller process on your machine using your current kubeconfig.

Important constraint:

- By default, the status syncer calls the in-cluster Service DNS name `pulse-probe-runner.<namespace>.svc`
- That DNS name is not resolvable from your laptop by default
- Reconciliation still creates cluster resources, but status syncing needs either an in-cluster controller or an explicit results URL override

Recommended local validation flow:

1. Install CRDs with `make install`
2. Apply a sample `HttpCanary`
3. Export the generated ConfigMap data to a local file
4. Export `auth.yaml`, then run the runner with `--auth-file=/tmp/pulse-auth.yaml`, `--listen=127.0.0.1:9090`, and `--api-listen=127.0.0.1:9091`
5. Run the controller with `PULSE_PROBE_RUNNER_RESULTS_URL=http://127.0.0.1:9091/results`

For end-to-end validation, deploying the controller into the cluster is the simpler and more representative path.

Operational APIs (`/results`, `/incidents`, `/topology`, and engine ingestion) require the reloadable token in `pulse-probe-auth.data.internal-token` on port 9091. Metrics and liveness remain unauthenticated at the application layer on port 9090 and are constrained by NetworkPolicy. Token rotation converges asynchronously as projected Secret volumes refresh; tolerate transient 401 responses during that bounded window, but require recovery without coordinated restarts.

Successful empty incident and proposal responses are authoritative: they clear obsolete canary intelligence and `inferredDependencies`. Fetch, authentication, or decode failures retain the last successful projection. An unreferenced policy also clears inferred dependencies after its reference count reaches zero. `lastSignalTime` is the newest signal for the active incident; timestamp-only updates are rate-limited to one minute, while material changes and closure project immediately.

## In-Cluster Validation Flow

1. Install the CRD
2. Build and publish a controller image
3. Build and publish a probe runner image
4. Deploy the controller manifests with `PROBE_RUNNER_IMAGE` set to the published runner image
5. Apply one or more sample `HttpCanary` resources
6. Inspect the runner StatefulSet, Services, optional incident engine, and canary status

## Useful Commands

```bash
kubectl get httpcanaries -A
kubectl get statefulset,deploy,svc,configmap -n pulse-system
kubectl describe httpcanary -n default sample-http-check
kubectl logs -n pulse-system deploy/pulse-controller-manager -c manager
kubectl logs -n pulse-system pulse-probe-runner-0
kubectl -n pulse-system port-forward svc/pulse-probe-runner 9091:9091
PULSE_INTERNAL_TOKEN=$(kubectl -n pulse-system get secret/pulse-probe-auth \
  -o jsonpath='{.data.internal-token}' | base64 --decode)
curl -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" http://127.0.0.1:9091/results
POD_NAMESPACE=pulse-system PULSE_PROBE_RUNNER_RESULTS_URL=http://127.0.0.1:9091/results make run
unset PULSE_INTERNAL_TOKEN
```

## Common Failure Cases

### CRD installed but no runtime resources

Likely causes:

- Controller is not running
- Controller lacks permissions
- The manager cannot authenticate to the cluster

### Runner exists but canary status stays empty

Likely causes:

- Status syncer cannot reach `/results`
- The runner pod never became Ready because its image could not be pulled
- Probe runner cannot load the config file
- The probe never completed yet

Read `/results` as a live surface. A persisted CR timestamp is not rewritten for every unchanged healthy check, so it may be older while live results are fresh. If a runner or the engine is restarting, missing results are absence of evidence—not a healthy zero score.

### Canary status is `Unhealthy`

Likely causes:

- Target URL is unreachable from inside the cluster
- Returned status does not match `expectedStatus`
- TLS or networking policy blocks egress from the runner pod

## Observability Expectations

- Controller logs show reconcile activity and status sync attempts
- Runner logs show config reloads and probe execution behavior
- `/metrics` exposes runner metrics for scraping

The current CR status does not automatically transition to `Unknown` when a result disappears from an otherwise reachable partial shard view. Operational tooling should alert on missing/stale live results; durable missing-result semantics remain a product decision.
