# Pulse Debug Environment Setup

This guide covers setting up and using the debug environment for the Pulse Kubebuilder operator.

## Prerequisites

- Go 1.25.3 or later
- kubebuilder (already installed at `/usr/local/bin/kubebuilder`)
- Docker (for container image building)
- VS Code with Go extension

## Environment Setup

All required tools are automatically installed in `./bin/` when you first build or run the project:
- `controller-gen` - Generates CRDs and RBAC rules from Go code
- `setup-envtest` - Manages Kubernetes test binaries
- `golangci-lint` - Linting with custom Go plugins
- `kustomize` - Kubernetes manifest management

## Local Development

### Running the Controller Locally

To run the controller against your current kubeconfig context:

```bash
make run
```

This will:
1. Generate manifests from code
2. Format and vet code
3. Start the controller manager locally

The controller will use your current kubeconfig to connect to the cluster.

### Running Tests

#### Unit Tests with EnvTest
```bash
make test
```

This runs all unit tests (excluding e2e tests) with an embedded Kubernetes API server and etcd.

#### E2E Tests with Kind
```bash
make test-e2e
```

This requires:
- Kind cluster (auto-created if needed)
- Docker running
- Will create a dedicated `pulse-test-e2e` cluster and clean up after completion

### Linting

```bash
# Run linter
make lint

# Auto-fix issues
make lint-fix
```

## VS Code Debugging

### 1. Debug Controller Locally

**Launch Configuration:** `Debug Controller`

Steps:
1. Set breakpoints in `internal/controller/` or `cmd/main.go`
2. Press `F5` or go to Run → Start Debugging
3. Select "Debug Controller" from the dropdown
4. Execution will pause at your breakpoints

### 2. Debug Unit Tests

**Launch Configuration:** `Debug Unit Tests`

Steps:
1. Set breakpoints in your test files
2. Press `F5` or go to Run → Start Debugging
3. Select "Debug Unit Tests"
4. Tests will run with debugger attached

You can modify the test filter in `.vscode/launch.json` by changing the `-test.run` pattern.

### 3. Debug with Go Runtime (Alternative)

**Launch Configuration:** `Debug with Go (Runtime)`

Steps:
1. Set breakpoints in code
2. Press `F5`
3. Select "Debug with Go (Runtime)"
4. Debugger attaches to running process

## Available VS Code Tasks

Press `Ctrl+Shift+B` to see available build tasks:

- **build-manager** (default build) - Generates code and builds binary
- **generate-and-manifest** - Generates code and CRDs
- **run-controller** - Runs controller locally
- **test** (default test) - Runs unit tests
- **lint** - Runs linter

## Code Generation Workflow

After modifying:
- **CRD types** (`api/*/` *_types.go files) → Run `make manifests generate`
- **RBAC markers** in controllers → Run `make manifests`
- **Any Go code** → Run `make fmt` and `make vet`

These are normally done automatically before `make build` and `make run`.

## Kubernetes Cluster Access

The controller uses your current kubeconfig:

```bash
# Use specific context
kubectl config use-context <context-name>

# Check current context
kubectl config current-context

# Apply test CRs
kubectl apply -k config/samples/
```

## Debugging Tips

### View Controller Logs
When running locally:
```bash
# Logs are printed to stdout
# Increase verbosity (requires modifying cmd/main.go)
```

When deployed:
```bash
kubectl logs -n pulse-system deployment/pulse-controller-manager -f
```

### Check Reconciliation
```bash
# Watch events
kubectl get events -n pulse-system -w

# Describe resource
kubectl describe <custom-resource>
```

### Inspect Generated Code
After running `make generate`:
- Check `api/*/zz_generated.*.go` for generated DeepCopy methods
- Check `config/crd/bases/*.yaml` for generated CRDs

### Environment Variables
Set when running `make run`:
```bash
DEVELOPMENT=true make run  # Adjust settings as needed
```

## Makefile Targets Reference

```bash
make help           # Show all available targets
make build          # Build manager binary
make run            # Run manager locally
make test           # Run unit tests
make test-e2e       # Run e2e tests
make manifests      # Generate CRDs/RBAC from code markers
make generate       # Generate code (DeepCopy, etc)
make docker-build   # Build Docker image
make docker-push    # Push Docker image
make fmt            # Format code
make vet            # Vet code
make lint           # Run linter
make lint-fix       # Auto-fix lint issues
```

## Troubleshooting

### envtest Not Found
```bash
make setup-envtest
```

### Port Already in Use
The controller uses port 8080 by default. Kill any existing process:
```bash
lsof -i :8080 | grep LISTEN | awk '{print $2}' | xargs kill
```

### Webhook Issues
If testing with webhooks locally, ensure:
1. Proper RBAC is configured
2. Webhook certificates are valid
3. Service is accessible from cluster

### Stale Cache
Clean build artifacts:
```bash
rm -rf bin/
make clean  # If available
```

Then rebuild:
```bash
make build
```

## Next Steps

1. Explore controllers in `internal/controller/`
2. Add custom logic to reconciliation
3. Create test cases in `*_test.go` files
4. Use debugger to step through reconciliation logic
5. Deploy to actual cluster when ready
