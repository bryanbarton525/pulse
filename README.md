# Pulse

<p align="center">
  <img src="docs/logo.jpg" alt="Pulse logo" width="480">
</p>

Pulse is a Kubernetes operator that lets developers define canary health checks as custom resources. Apply a YAML file, and Pulse continuously monitors your endpoints and reports status back on the CR.

Pulse supports both simple single-request checks and scripted multi-step HTTP journeys for login, session, and checkout-style flows.

The repository already includes a fuller design set in `docs/`. Start with the architecture summary, then drill into reconciliation, scaling, operations, and validation details.

## Quick Start

```bash
# Install CRDs
make install

# Run the controller locally
make run

# Apply a canary check
kubectl apply -f config/samples/canary_v1alpha1_httpcanary.yaml

# Watch the status
kubectl get httpcanaries -w
```

## Example

```yaml
apiVersion: canary.iambarton.com/v1alpha1
kind: HttpCanary
metadata:
  name: check-my-api
spec:
  url: "https://api.example.com/health"
  interval: 30
  expectedStatus: 200
```

```yaml
apiVersion: canary.iambarton.com/v1alpha1
kind: HttpCanary
metadata:
  name: check-login-journey
spec:
  url: "https://example.com/dashboard"
  interval: 30
  expectedStatus: 200
  containsText: "dashboard"
  journey:
    - name: open-login
      url: "https://example.com/login"
      method: GET
      expectedStatus: 200
      containsText: "Sign in"
    - name: submit-login
      url: "https://example.com/session"
      method: POST
      headers:
        Content-Type: application/json
      body: '{"username":"demo","password":"secret"}'
      expectedStatus: 200
      containsText: "dashboard"
```

```
$ kubectl get httpcanaries
NAME            URL                                PHASE     AGE
check-my-api    https://api.example.com/health     Healthy   5m
```

## How It Works

Pulse uses a split architecture for scalability:

1. **Controller** watches HttpCanary CRs across all namespaces and manages a shared probe configuration (ConfigMap), a probe runner Deployment, and a Service
2. **Probe Runner** reads the config, executes HTTP checks on each probe's interval, and exposes results via a `/results` endpoint
3. **Status Syncer** polls the runner every 15 seconds and writes results back to each CR's `.status`

This separation keeps the operator lightweight (it never makes HTTP calls itself) and allows the probe runner to scale independently.

For richer synthetics, the probe runner reuses one HTTP client and cookie jar per journey so multi-step session flows work without adding browser automation to the operator.

## Supported Canary Types

| Kind | Description | Status |
|------|-------------|--------|
| `HttpCanary` | HTTP endpoint health checks | Implemented |
| `TcpCanary` | TCP port connectivity checks | Planned |
| `GrpcCanary` | gRPC health protocol checks | Planned |

## Documentation

- [Architecture Summary](docs/architecture-summary.md) -- concise system model and component responsibilities
- [Architecture](docs/architecture.md) -- component overview and data flow
- [Reconciliation Design](docs/reconciliation-design.md) -- why reconcile is single-key and infrastructure-focused
- [CRD Design](docs/crd-design.md) -- API schema, versioning, and how to add new CRD types
- [HTTP Journey Guide](docs/http-journey-canary.md) -- exact runtime semantics and authoring patterns for multi-step HTTP canaries
- [Helm Guide](docs/helm.md) -- chart install flow, private GHCR pulls, and sample probe usage
- [Scaling Design](docs/scaling.md) -- how the controller handles thousands of canaries
- [Operations Guide](docs/operations.md) -- cluster runtime model, inspection, and troubleshooting
- [Testing and Validation](docs/testing-and-validation.md) -- automated checks and cluster smoke-test flow
- [Development Guide](docs/development.md) -- building, testing, and debugging

## Building

```bash
make build                    # Build controller binary
make build-proberunner        # Build probe runner binary
make manifests                # Regenerate CRD + RBAC YAML
make generate                 # Regenerate DeepCopy methods
make docker-build IMG=...     # Build controller container image
make docker-build-proberunner # Build probe runner container image
make helm-deploy IMG=... PROBE_RUNNER_IMAGE=... # Install the operator with Helm
make test                     # Run unit tests
make test-e2e                 # Run e2e tests (requires Kind)
```

## Helm Deploy

```bash
make helm-deploy \
  IMG=ghcr.io/bryanbarton525/pulse-controller:latest \
  PROBE_RUNNER_IMAGE=ghcr.io/bryanbarton525/pulse-probe-runner:latest

kubectl apply -f config/samples/canary_v1alpha1_httpcanary.yaml
kubectl apply -f config/samples/canary_v1alpha1_httpcanary_204.yaml
kubectl apply -f config/samples/canary_v1alpha1_httpcanary_unhealthy.yaml
kubectl apply -f config/samples/canary_v1alpha1_httpcanary_ui_login.yaml
kubectl apply -f config/samples/canary_v1alpha1_httpcanary_api_readiness.yaml
kubectl apply -f config/samples/canary_v1alpha1_httpcanary_checkout_entry.yaml
kubectl get httpcanaries -A
```

If your GHCR packages are private, create a pull secret and pass `HELM_IMAGE_PULL_SECRET=ghcr-pull-secret`. See `docs/helm.md` for the full flow.

The UI/login and checkout examples now exercise scripted HTTP journeys. They remain HTTP-only and do not execute browser-based automation.

## Cluster Validation

For a quick reconcile-only smoke test against a cluster:

```bash
make install
make run
kubectl apply -f config/samples/canary_v1alpha1_httpcanary.yaml
kubectl get httpcanaries -A -w
```

For full status propagation from a locally running controller, start a local probe runner and override the results URL:

```bash
kubectl get configmap pulse-probe-config -n pulse-system -o jsonpath='{.data.probes\.yaml}' > /tmp/pulse-probes.yaml
./bin/probe-runner --config=/tmp/pulse-probes.yaml --listen=127.0.0.1:9090
POD_NAMESPACE=pulse-system \
PULSE_PROBE_RUNNER_RESULTS_URL=http://127.0.0.1:9090/results \
make run
kubectl get httpcanary sample-http-check -n default -o yaml
```

For a fully in-cluster deployment, the cluster still needs access to a real probe runner image. See `docs/testing-and-validation.md` and `docs/operations.md` for the full flow.

## Project Info

- **Domain:** `iambarton.com`
- **API Group:** `canary.iambarton.com`
- **Built with:** Kubebuilder v4, controller-runtime v0.23
- **Go version:** 1.25+

## License

This repository is licensed under the Apache License 2.0. See `LICENSE`.

## Releases

Pushing a version tag such as `v0.2.0` publishes the controller and probe-runner images to GHCR and creates a GitHub Release with:

- a versioned install manifest
- a packaged Helm chart
- a checksums file for the release assets
