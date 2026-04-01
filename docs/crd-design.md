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
| `interval` | int | No | 30 | minimum=5 | Check frequency in seconds |
| `expectedStatus` | int | No | 200 | 100-599 | HTTP status code that means healthy |

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
