# Operations Guide

This document covers how to run, inspect, and troubleshoot Pulse on a real cluster.

## Namespaces and Resources

- The operator defaults to `pulse-system` for shared infrastructure.
- `HttpCanary` resources can be created in any namespace.
- The controller creates these runtime resources in the operator namespace:
  - `ConfigMap/pulse-probe-config`
  - `Deployment/pulse-probe-runner`
  - `Service/pulse-probe-runner`

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
4. Run `./bin/probe-runner --config=/tmp/pulse-probes.yaml --listen=127.0.0.1:9090`
5. Run the controller with `PULSE_PROBE_RUNNER_RESULTS_URL=http://127.0.0.1:9090/results`

For end-to-end validation, deploying the controller into the cluster is the simpler and more representative path.

## In-Cluster Validation Flow

1. Install the CRD
2. Build and publish a controller image
3. Build and publish a probe runner image
4. Deploy the controller manifests with `PROBE_RUNNER_IMAGE` set to the published runner image
5. Apply one or more sample `HttpCanary` resources
6. Inspect the runner Deployment, Service, and canary status

## Useful Commands

```bash
kubectl get httpcanaries -A
kubectl get deploy,svc,configmap -n pulse-system
kubectl describe httpcanary -n default sample-http-check
kubectl logs -n pulse-system deploy/pulse-controller-manager -c manager
kubectl logs -n pulse-system deploy/pulse-probe-runner
kubectl -n pulse-system port-forward svc/pulse-probe-runner 9090:9090
curl http://127.0.0.1:9090/results
POD_NAMESPACE=pulse-system PULSE_PROBE_RUNNER_RESULTS_URL=http://127.0.0.1:9090/results make run
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

### Canary status is `Unhealthy`

Likely causes:

- Target URL is unreachable from inside the cluster
- Returned status does not match `expectedStatus`
- TLS or networking policy blocks egress from the runner pod

## Observability Expectations

- Controller logs show reconcile activity and status sync attempts
- Runner logs show config reloads and probe execution behavior
- `/metrics` exposes runner metrics for scraping
