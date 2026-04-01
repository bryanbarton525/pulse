# CRD Design

## API Group

All Pulse CRDs belong to the API group `canary.iambarton.com`. The current version is `v1alpha1` (unstable, breaking changes expected).

## Design Principles

1. **One CRD per canary type.** `HttpCanary`, `TcpCanary`, `GrpcCanary` are separate Kinds, not a single `Canary` with a `type` field. This gives each type its own schema, validation, and defaulting.

2. **Spec is user-owned, Status is controller-owned.** The `+kubebuilder:subresource:status` marker enforces this at the API level. Users cannot modify `.status`; the controller cannot accidentally overwrite `.spec`.

3. **Sensible defaults via markers.** Fields like `interval` and `expectedStatus` have `+kubebuilder:default` values so a minimal CR only needs the `url` field.

4. **Server-side validation.** All validation happens at the API server via OpenAPI v3 schema rules generated from `+kubebuilder:validation` markers. The controller never receives invalid objects.

## HttpCanary

### Spec Fields

| Field | Type | Required | Default | Validation | Description |
|-------|------|----------|---------|------------|-------------|
| `url` | string | Yes | — | minLength=1 | HTTP endpoint to check |
| `method` | string | No | `GET` | `GET`, `POST`, `PUT`, `PATCH`, `DELETE`, `HEAD` | HTTP method for simple single-request mode |
| `headers` | map[string]string | No | — | — | Request headers for simple single-request mode |
| `auth` | HttpCanaryAuth | No | — | `type` must be `basic`, `bearer`, or `apiKey` | Secret-backed auth for simple HTTP, journey, and MCP probes |
| `body` | string | No | — | — | Request body for simple single-request mode |
| `interval` | int | No | 30 | minimum=5 | Check frequency in seconds |
| `expectedStatus` | int | No | 200 | 100-599 | HTTP status code that means healthy |
| `containsText` | string | No | — | — | Required response-body substring for simple single-request mode |
| `mcp` | HttpCanaryMCP | No | — | — | MCP initialize plus tools/list validation over HTTP |
| `journey` | []HttpCanaryStep | No | — | step `name` and `url` required | Ordered multi-step HTTP journey |
| `outputs` | []HttpCanaryOutput | No | `[{type: prometheus}]` | `type` must be `prometheus` or `stdout` | Per-canary telemetry sinks |

### HttpCanaryAuth Fields

| Field | Type | Required | Validation | Description |
|-------|------|----------|------------|-------------|
| `type` | string | Yes | `basic`, `bearer`, `apiKey` | Auth strategy |
| `basic.usernameSecretRef` | SecretKeySelector | With `type: basic` | Secret ref required by runtime | Username for HTTP Basic auth |
| `basic.passwordSecretRef` | SecretKeySelector | With `type: basic` | Secret ref required by runtime | Password for HTTP Basic auth |
| `bearer.tokenSecretRef` | SecretKeySelector | With `type: bearer` | Secret ref required by runtime | Bearer token, including JWT-style tokens |
| `apiKey.headerName` | string | With `type: apiKey` | minLength=1 | Header that receives the Secret value |
| `apiKey.valueSecretRef` | SecretKeySelector | With `type: apiKey` | Secret ref required by runtime | Secret-backed header value |

### HttpCanaryMCP Fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `protocolVersion` | string | No | `2025-11-25` | Protocol version announced in `initialize` |
| `clientName` | string | No | `pulse` | Client name sent in `initialize` |
| `clientVersion` | string | No | `0.1.0` | Client version sent in `initialize` |
| `requireToolsCapability` | bool | No | `true` | Require `initialize` to advertise tool support |
| `minToolCount` | int | No | `0` | Minimum number of tools returned by `tools/list` |
| `requiredTools` | []string | No | — | Tool names that must be present in `tools/list` |

### HttpCanaryOutput Fields

| Field | Type | Required | Default | Validation | Description |
|-------|------|----------|---------|------------|-------------|
| `type` | string | Yes | — | `prometheus`, `stdout` | Where Pulse emits probe telemetry for the canary |

### HttpCanaryStep Fields

| Field | Type | Required | Default | Validation | Description |
|-------|------|----------|---------|------------|-------------|
| `name` | string | Yes | — | minLength=1 | Human-readable step label |
| `url` | string | Yes | — | minLength=1 | Endpoint called for the step |
| `method` | string | No | `GET` | `GET`, `POST`, `PUT`, `PATCH`, `DELETE`, `HEAD` | HTTP method for the step |
| `headers` | map[string]string | No | — | — | Request headers for the step |
| `body` | string | No | — | — | Request body for the step |
| `expectedStatus` | int | No | 200 | 100-599 | HTTP status code that means success for the step |
| `containsText` | string | No | — | — | Required response-body substring for the step |

### Journey Execution Rules

- If `journey` is empty, Pulse executes one request using the top-level `url`, `method`, `headers`, `body`, `expectedStatus`, and `containsText` fields.
- If `journey` is present, Pulse executes the steps in order and does not execute the top-level request fields.
- If `auth` is present, Pulse resolves the referenced Secret values in the canary namespace and applies the generated auth headers at runtime.
- If `mcp` is present, Pulse ignores `method`, `body`, `expectedStatus`, and `containsText` for execution and performs `initialize`, `notifications/initialized`, and `tools/list` over HTTP instead.
- Journey steps share cookies within a single check cycle, but not across intervals.
- The top-level `url` remains required and is still the URL shown in `kubectl get httpcanaries`, so it should represent the canary's primary or final target.
- If `outputs` is omitted, Pulse emits Prometheus metrics for backward compatibility.
- `stdout` output writes one JSON line per check result, which can be collected by log-based agents such as Datadog daemonsets or sidecars.

For step-by-step authoring guidance and examples, see [HTTP Journey Guide](http-journey-canary.md).

### Status Fields

| Field | Type | Description |
|-------|------|-------------|
| `phase` | string | `Healthy`, `Unhealthy`, or `Unknown` |
| `lastCheckTime` | time | When the last check ran |
| `lastStatus` | int | HTTP status code from the last check |
| `message` | string | Human-readable detail |

### Print Columns

`kubectl get httpcanaries` displays:

```
NAME              URL                              PHASE     AGE
check-my-api      https://api.example.com/health   Healthy   5m
```

### Example CR

```yaml
apiVersion: canary.iambarton.com/v1alpha1
kind: HttpCanary
metadata:
  name: check-my-api
  namespace: default
spec:
  url: "https://api.example.com/health"
  interval: 30
  expectedStatus: 200
  outputs:
    - type: prometheus
```

## Adding a New CRD

To add a new canary type (e.g., `TcpCanary`):

1. Create `api/v1alpha1/tcpcanary_types.go` with Spec, Status, and the Kind struct
2. Add `+kubebuilder:object:root=true` and `+kubebuilder:subresource:status` markers
3. Register with `SchemeBuilder.Register()` in an `init()` function
4. Add a corresponding `Probe` type variant in `internal/proberunner/config.go`
5. Add the check logic in `internal/proberunner/runner.go`
6. Run `make generate` (DeepCopy) then `make manifests` (CRD YAML + RBAC)
7. Update the reconciler to include the new type in `buildProbeConfig()`

The controller's single-key reconcile pattern and the StatusSyncer work across CRD types without modification — they operate on the shared ConfigMap and `/results` endpoint.

## Versioning Strategy

- `v1alpha1` — current, unstable. Schema may change without migration support.
- `v1beta1` — future, once the API stabilizes. Conversion webhooks will handle v1alpha1 → v1beta1.
- `v1` — stable. No breaking changes without a new API group version.

Kubebuilder supports multiple versions via the `+kubebuilder:storageversion` marker. Only one version is the storage version (persisted in etcd); others are served via conversion.
