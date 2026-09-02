# Pulse

<p align="center">
  <img src="docs/logo.jpg" alt="Pulse logo" width="480">
</p>

Pulse is a Kubernetes operator that lets developers define canary health checks as custom resources. Apply a YAML file, and Pulse continuously monitors your endpoints and reports status back on the CR.

Pulse supports simple single-request checks, scripted multi-step HTTP journeys for login, session, and checkout-style flows, and MCP tool-availability validation over HTTP.

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
  outputs:
    - type: prometheus
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

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: mcp-auth
type: Opaque
stringData:
  token: replace-me
---
apiVersion: canary.iambarton.com/v1alpha1
kind: HttpCanary
metadata:
  name: check-mcp-tools
spec:
  url: "https://mcp.example.com/mcp"
  interval: 30
  auth:
    type: bearer
    bearer:
      tokenSecretRef:
        name: mcp-auth
        key: token
  headers:
    Accept: application/json, text/event-stream
  mcp:
    protocolVersion: "2025-11-25"
    clientName: pulse
    clientVersion: 0.1.0
    requireToolsCapability: true
    minToolCount: 1
    requiredTools:
      - health.check
  outputs:
    - type: prometheus
    - type: stdout
```

```
$ kubectl get httpcanaries
NAME            URL                                PHASE     AGE
check-my-api    https://api.example.com/health     Healthy   5m
```

## How It Works

Pulse uses a split architecture for scalability:

1. **Controller** watches HttpCanary, GrpcCanary, and AnomalyPolicy CRs across all namespaces and manages a shared probe configuration (ConfigMap), a probe runner StatefulSet, and Services
2. **Probe Runner** reads the generated config and auth material, executes checks on each probe's interval, exposes `/results` for status sync, and can emit telemetry per canary to Prometheus, stdout, or both
3. **Status Syncer** polls every 15 seconds and writes results back to each CR's `.status`
4. **Incident Engine** (optional) correlates failures across canaries and performs policy actions -- only deployed when a canary opts into [Model Intelligence](#model-intelligence)

This separation keeps the operator lightweight (it never makes HTTP calls itself) and allows the probe runner to scale independently.

Probe runners shard by a stable hash of the probe name against their StatefulSet ordinal, so scaling to N replicas partitions the checks with no coordination, no leases, and no shared state. One replica -- the default -- owns everything and reproduces the original single-pod behavior exactly.

For richer synthetics, the probe runner reuses one HTTP client and cookie jar per journey so multi-step session flows work without adding browser automation to the operator.

## Output Sinks

`HttpCanary.spec.outputs` controls where execution telemetry is emitted for each canary.

- `prometheus` keeps the current behavior and exports probe metrics on the runner's `/metrics` endpoint
- `stdout` writes one JSON result line per check, which is useful for log collection paths such as Datadog daemonsets or sidecars
- Omitting `outputs` defaults to `prometheus` for backward compatibility

The internal `/results` endpoint remains in place for the controller's status sync loop and is not configured per canary.

## MCP Canaries

Pulse now supports first-class MCP validation for HTTP-based MCP servers.

- Pulse performs a real MCP handshake: `initialize`, `notifications/initialized`, and `tools/list`
- Health can require tool capability support, a minimum tool count, specific required tool names, or all three together
- Auth is Secret-backed and currently supports `basic`, `bearer`, and `apiKey`
- MCP support is HTTP-only in this release; stdio transport is not implemented
- OAuth 2.1 token acquisition is not implemented yet, so secured endpoints should use pre-provisioned credentials in Kubernetes Secrets

The controller keeps secret material out of the shared probe ConfigMap by resolving referenced Secret keys into a generated Secret mounted only into the probe runner.

## Model Intelligence

Pulse is bring-your-own-canary. This extends it with **bring-your-own-evaluation**: an
opt-in layer that runs an embedded model over check results. It is entirely optional --
a canary without `spec.intelligence` behaves exactly as it always has, and a cluster with
no `AnomalyPolicy` deploys no extra pods.

### Why not "detect anomalous failures"

The obvious design -- embed the failure message, alert when it looks unusual -- does not
work here, and it is worth saying why. A canary's failure is **deterministic by
construction**: it fails when `expectedStatus` or `containsText` says it should. The runner
synthesizes those messages from a handful of format strings, so after normalization
`Expected 200 but got 503` and `Expected 200 but got 502` are the same string. Running
cosine math over a dozen possible values is a `switch` statement wearing a model.

The signal lives in two places the assertions cannot reach: the **response body**, and the
**relationships between concurrent failures**.

### The four triggers

| Trigger | Question it answers | Runs on | Model |
|---------|--------------------|---------|-------|
| `bodyDrift` | The check **passed** -- did the response body's meaning change anyway? | every passing check | static embeddings |
| `latencyShift` | Green, but is it getting slower? | every check | none, pure statistics |
| `failureCorrelation` | Are these five failures **one incident or five**? Which is the root cause? | failures only | transformer |
| `failureNovelty` | Have we **seen this failure shape before**? | failures only | transformer |

`bodyDrift` and `latencyShift` catch **green-but-wrong** -- an empty result set, a stack
trace inside a 200, a maintenance page, a creeping p99. You cannot pre-write an assertion
for these because you do not know in advance what wrong will look like.

The default drift threshold of `0.15` is measured, not guessed. Against a JSON payload whose
item count, field values, and timestamps all vary between checks:

| | drift score |
|---|---|
| healthy variation (median / p95 / **max**) | 0.015 / 0.052 / **0.063** |
| empty result set | 0.200 |
| truncated JSON | 0.204 |
| null collection | 0.286 |
| stack trace inside a 200 | 0.473 |
| maintenance page | 0.591 |

That is a 3.3x gap between the worst healthy check and the weakest real failure, and it is
pinned by a regression test so a change to normalization or the bundled model cannot quietly
invalidate the default.

`failureCorrelation` and `failureNovelty` do **triage**: collapse a storm into one incident
with one investigation, and keep the four hundredth repeat of a known failure quiet.

### Correlation is evidence-gated

Two canaries failing at the same moment is **not** evidence of anything -- in a cluster of a
few thousand checks, unrelated things fail simultaneously constantly. A merge requires an
actual reason:

1. a **declared dependency** in `spec.topology.dependsOn`, or
2. **failure text that says both checks hit the same wall** -- two canaries with no declared
   relationship reporting an identical dial timeout are telling you they share an upstream
   nobody wrote down

Because the gate is evidence rather than configuration, correlation works across canaries
governed by *different* policies. That matters: policies follow team ownership, but outages
follow infrastructure, and a payments canary and its database canary are rarely owned by the
same team.

The root cause is the failing canary with **no failing upstream**. Its policy owns the
incident and fires the full action set; downstream victims are suppressed unless their own
policy sets `incidents.notifyOnDownstream`.

Pulse also **learns** dependency edges from co-occurrence, but only ever *proposes* them in
`AnomalyPolicy.status.inferredDependencies`. A proposal never affects correlation until a
human copies it into `spec.topology.dependsOn` -- co-occurrence is not causation, and an
edge that silently redirected blame would send someone to debug the wrong service.

### Two models, because the paths differ by 1000x

`bodyDrift` scores every passing check; correlation only sees failures. At 5,000 canaries on
a 30s interval that is ~167/sec versus ~0.17/sec. A transformer on the hot path would burn
2-4 cores continuously to track a signal that moves over hours.

- **Hot path**: `potion-base-32M` (Model2Vec). Inference is a token lookup and a mean -- no
  transformer forward pass, **no cgo**. Benchmarked at ~12,000 embeddings/sec/core, and a
  cache in front absorbs the common case where an endpoint returns an identical body every
  time. The probe runner keeps its `CGO_ENABLED=0` `distroless/static` image.
- **Cold path**: `all-MiniLM-L6-v2` via ONNX Runtime, in the single-replica incident engine.
  This is the only image with cgo and a transformer.

Vectors from the two models live in different embedding spaces and are never comparable;
mixing them panics rather than silently producing a plausible-looking number. For the same
reason the cold-path model is **cluster-wide rather than per-policy** -- correlation compares
failures across policies, so per-policy models would make that either silently wrong or
impossible. Policies that disagree are resolved deterministically and the conflict logged.

### Response bodies never leave the probe runner

Bodies are embedded in-process and compared in-process. What crosses the wire is a
**score**, plus normalized and redacted failure text. Bodies are excluded from `/results`,
from CR status, and from the model prompt. `triggers.bodyDrift.redact` takes regexes applied
before anything is embedded or logged.

### Actions

Actions fire in declared order for the root cause's policy, so an `llm` action can populate
the analysis that a later `slack` action includes.

| Type | What it does |
|------|--------------|
| `metric` | Prometheus gauge plus a Kubernetes Event. No credentials -- always works |
| `llm` | One investigation per **incident** (not per canary) against any OpenAI-compatible chat endpoint |
| `slack` | Incoming webhook or `chat.postMessage`, with an optional Go template |
| `observability` | Ships to `datadog`, `loki`, `elasticsearch`, `splunk`, `otlp`, or a `generic` webhook |

A throttle keyed on the incident's *shape* keeps a flapping canary from paging repeatedly.

### Getting started

```bash
# Download and convert the models baked into the images (~210 MiB).
make fetch-models

make docker-build-proberunner docker-build-incidentengine

# The smallest useful policy needs no credentials at all.
kubectl apply -f config/samples/canary_v1alpha1_anomalypolicy_minimal.yaml
```

Then opt a canary in:

```yaml
spec:
  intelligence:
    policyRef:
      name: basic-triage
      namespace: pulse-system
```

See `config/samples/canary_v1alpha1_anomalypolicy.yaml` for every option, and the
[Model Intelligence Guide](docs/model-intelligence.html) for a from-scratch explanation of
embeddings, cosine distance, and how a drift score is calculated and turned into a signal.

> **Note on SGLang**: a single SGLang instance is either generative *or* an embedder, never
> both. Your chat deployment is the `llm` action target; it will reject `/v1/embeddings`
> unless launched with `--is-embedding`. Embeddings come from the in-process models by
> default, so no separate service is required.

## Supported Canary Types

| Kind | Description | Status |
|------|-------------|--------|
| `HttpCanary` | HTTP endpoint health checks | Implemented |
| `GrpcCanary` | gRPC health protocol checks | Implemented |
| `AnomalyPolicy` | Model-driven evaluation and incident actions | Implemented |
| `TcpCanary` | TCP port connectivity checks | Planned |

## Documentation

- [Architecture Summary](docs/architecture-summary.md) -- concise system model and component responsibilities
- [Architecture](docs/architecture.md) -- component overview and data flow
- [Reconciliation Design](docs/reconciliation-design.md) -- why reconcile is single-key and infrastructure-focused
- [CRD Design](docs/crd-design.md) -- API schema, versioning, and how to add new CRD types
- [HTTP Journey Guide](docs/http-journey-canary.md) -- exact runtime semantics and authoring patterns for multi-step HTTP canaries
- [Helm Guide](docs/helm.md) -- chart install flow, private GHCR pulls, and sample probe usage
- [Model Intelligence Guide](docs/model-intelligence.html) -- the vector maths behind drift and correlation from first principles, the deploy runbook, and operating notes (HTML; open it locally, GitHub shows the source rather than rendering it)
- [Scaling Design](docs/scaling.md) -- how the controller handles thousands of canaries
- [Operations Guide](docs/operations.md) -- cluster runtime model, inspection, and troubleshooting
- [Testing and Validation](docs/testing-and-validation.md) -- automated checks and cluster smoke-test flow
- [Development Guide](docs/development.md) -- building, testing, and debugging

## Building

```bash
make build                       # Build controller binary
make build-proberunner           # Build probe runner binary
make build-incidentengine        # Build incident engine binary
make fetch-models                # Download + convert the embedding models (~210 MiB)
make manifests                   # Regenerate CRD + RBAC YAML
make generate                    # Regenerate DeepCopy methods
make docker-build IMG=...        # Build controller container image
make docker-build-proberunner    # Build probe runner container image
make docker-build-incidentengine # Build incident engine container image
make helm-deploy IMG=... PROBE_RUNNER_IMAGE=... # Install the operator with Helm
make test                        # Run unit tests
make test-e2e                    # Run e2e tests (requires Kind)
```

The incident engine is the only component that needs cgo and ONNX Runtime, and only when
built with the `onnx` tag (which `make docker-build-incidentengine` does). Everything else,
including the whole test suite, builds and runs with `CGO_ENABLED=0` and no native
dependencies:

```bash
CGO_ENABLED=0 go build ./...    # controller + probe runner, no native deps
go test ./internal/...          # full suite, no models required
```

Tests that need real model weights skip automatically until `make fetch-models` has run.

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
kubectl apply -f config/samples/canary_v1alpha1_httpcanary_mcp.yaml
kubectl get httpcanaries -A
```

If your GHCR packages are private, create a pull secret and pass `HELM_IMAGE_PULL_SECRET=ghcr-pull-secret`. See `docs/helm.md` for the full flow.

The UI/login and checkout examples exercise scripted HTTP journeys. The MCP sample performs a real initialize plus tools/list validation against an MCP HTTP endpoint and supports Secret-backed Basic, Bearer/JWT, and API key authentication. These remain HTTP-only and do not execute browser-based automation or OAuth login flows.

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
kubectl get secret pulse-probe-auth -n pulse-system -o jsonpath='{.data.auth\.yaml}' | base64 --decode > /tmp/pulse-auth.yaml
./bin/probe-runner --config=/tmp/pulse-probes.yaml --auth-file=/tmp/pulse-auth.yaml --listen=127.0.0.1:9090
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
