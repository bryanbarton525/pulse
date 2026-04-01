# Testing And Validation

Pulse needs two levels of validation: code-level correctness and cluster-level behavior.

## Automated Checks

### Unit and envtest suite

Use the standard test target for API and controller logic that can run against envtest:

```bash
make test
```

This excludes the end-to-end suite and exercises packages outside `test/e2e`.

### Linting

```bash
make lint
```

### E2E on isolated Kind cluster

The repository already documents this as the supported end-to-end path:

```bash
make test-e2e
```

Use an isolated Kind cluster rather than a shared development cluster.

## Manual Cluster Validation

For a real cluster smoke test, validate these stages in order:

1. CRD install succeeds
2. Controller starts without auth or RBAC errors
3. Creating an `HttpCanary` produces the shared ConfigMap, Deployment, and Service
4. Probe runner becomes Ready
5. `/results` returns probe data
6. `HttpCanary.status` transitions to `Healthy` or `Unhealthy`

## Recommended Smoke Test

Apply the sample resource:

```bash
kubectl apply -f config/samples/canary_v1alpha1_httpcanary.yaml
kubectl get httpcanaries -A -w
```

Then inspect:

```bash
kubectl get configmap pulse-probe-config -n pulse-system -o yaml
kubectl get deployment pulse-probe-runner -n pulse-system
kubectl logs -n pulse-system deploy/pulse-probe-runner
kubectl get httpcanary sample-http-check -n default -o yaml
```

For an in-cluster deployment, confirm the cluster can actually pull the configured probe runner image. The repository now includes `Dockerfile.proberunner` and `make docker-build-proberunner`, but the image still needs to be published or otherwise made available to your cluster runtime.

## What Success Looks Like

- The runner config contains the canary you applied
- The runner pod is Ready
- `/results` includes an entry for `default/sample-http-check`
- The CR status shows a recent check time and the expected HTTP status

## Local Validation Path

When the controller runs outside the cluster with `make run`, reconciliation still works. To make status sync work as well, run a local probe runner and point the controller at it:

```bash
kubectl get configmap pulse-probe-config -n pulse-system -o jsonpath='{.data.probes\.yaml}' > /tmp/pulse-probes.yaml
./bin/probe-runner --config=/tmp/pulse-probes.yaml --listen=127.0.0.1:9090
POD_NAMESPACE=pulse-system \
PULSE_PROBE_RUNNER_RESULTS_URL=http://127.0.0.1:9090/results \
make run
```

This path was validated against the active cluster by confirming `sample-http-check` reached `phase: Healthy` with a populated `lastCheckTime`, `lastStatus`, and `message`.

The best proof of correctness is still one of:

- a full in-cluster deployment, or
- the existing Kind-based end-to-end suite

In both cases, the probe runner image still has to be available to the cluster.
