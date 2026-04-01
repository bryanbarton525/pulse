# Development Guide

## Prerequisites

- Go 1.25+
- Docker (for building images)
- kubectl configured to a cluster
- Kind (for local testing)

## Project Structure

```
pulse/
  api/v1alpha1/              # CRD type definitions
    groupversion_info.go      # API group registration
    httpcanary_types.go       # HttpCanary Spec + Status
    zz_generated.deepcopy.go  # Auto-generated (do not edit)
  cmd/
    main.go                   # Controller manager entrypoint
    proberunner/main.go       # Probe runner entrypoint
  internal/
    controller/
      httpcanary_controller.go  # Reconciler: manages infrastructure
      status_syncer.go          # Background status polling
    proberunner/
      config.go                 # Probe config types + file watcher
      runner.go                 # HTTP check execution
      server.go                 # /results, /metrics, /healthz endpoints
  config/
    crd/bases/                  # Generated CRD YAML
    rbac/                       # Generated RBAC roles
    manager/                    # Controller Deployment spec
    samples/                    # Example CRs
  docs/                         # Design documents
```

## Common Tasks

### Build

```bash
# Build the controller binary
make build

# Build the probe runner binary
go build -o bin/probe-runner cmd/proberunner/main.go
```

### Code Generation

```bash
# After modifying *_types.go files:
make generate    # Regenerates DeepCopy methods
make manifests   # Regenerates CRD YAML + RBAC ClusterRole
```

Always run both after changing type definitions or RBAC markers.

### Run Locally (against a cluster)

```bash
# Install CRDs into the cluster
make install

# Run the controller on your machine (uses ~/.kube/config)
make run

# In another terminal, apply a sample canary
kubectl apply -f config/samples/canary_v1alpha1_httpcanary.yaml

# Watch it
kubectl get httpcanaries -w
```

When running locally, the controller creates the probe runner Deployment in-cluster but the StatusSyncer cannot reach the Service DNS. Use `kubectl port-forward` to bridge the gap, or test with `make deploy` instead.

### Deploy to Cluster

```bash
# Build and push images
make docker-build IMG=your-registry/pulse-controller:latest
docker build -t your-registry/pulse-probe-runner:latest -f Dockerfile.proberunner .

# Deploy
make deploy IMG=your-registry/pulse-controller:latest

# Verify
kubectl -n pulse-system get pods
```

### Run Tests

```bash
# Unit tests
make test

# End-to-end tests (creates a Kind cluster)
make test-e2e
```

### Lint

```bash
make lint        # Check for issues
make lint-fix    # Auto-fix what's possible
```

## Adding a New CRD

See [CRD Design](crd-design.md) for the full process. Quick checklist:

1. Create `api/v1alpha1/<kind>_types.go`
2. Add markers: `+kubebuilder:object:root=true`, `+kubebuilder:subresource:status`
3. Register in `init()`: `SchemeBuilder.Register(&Kind{}, &KindList{})`
4. Add probe type in `internal/proberunner/config.go`
5. Add check logic in `internal/proberunner/runner.go`
6. `make generate && make manifests`
7. Update the reconciler's `buildProbeConfig()`

## Debugging

### Controller logs

```bash
# When running locally
make run 2>&1 | grep -E 'INFO|ERROR'

# When deployed
kubectl -n pulse-system logs -l control-plane=controller-manager -f
```

### Probe runner logs

```bash
kubectl -n pulse-system logs -l app.kubernetes.io/name=pulse-probe-runner -f
```

### Check CRD is installed

```bash
kubectl get crd httpcanaries.canary.iambarton.com
```

### Check CR status

```bash
kubectl get httpcanary <name> -o yaml
```

### Probe runner results

```bash
kubectl -n pulse-system port-forward svc/pulse-probe-runner 9090:9090
curl http://localhost:9090/results | jq .
curl http://localhost:9090/metrics
```
