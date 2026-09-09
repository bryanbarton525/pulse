# Install the controller by hand

Start from the Ready `pulse-book` cluster and prepared model artifacts from the previous chapters. Run commands from the Pulse repository root. This chapter installs the deterministic monitoring components first. There is no policy yet, so the incident engine is not needed to run the first canary.

## Build the processes you will run

The controller image runs reconciliation and status projection. The runner image executes checks. A third image provides a small Go application that we can deliberately break in later exercises.

With Podman, build each image explicitly:

```sh
podman build -f Dockerfile -t localhost/pulse-controller:book-v1 .
podman build -f Dockerfile.proberunner -t localhost/pulse-probe-runner:book-v1 .
podman build -f Dockerfile.demo-target -t localhost/pulse-demo-target:book-v1 .
```

With Docker, execute the same three commands with `docker` instead of `podman`. Keep the explicit `localhost/` image names in both cases so the manifests do not change between providers. These are local image names, not a request to push to a registry. If `go mod download` times out reaching `proxy.golang.org` from BuildKit but works on the host, add `--network=host` to each `docker build` command.

The runner Dockerfile copies the prepared Potion files. A deterministic canary does not require those weights, and no model is loaded until a policy enables body drift. Do not interpret this image build alone as model-load evidence; the intelligence installation chapter checks the runtime logs before any model experiment.

The tag `book-v1` identifies this laboratory build. Record `git rev-parse HEAD` in your lab notes. When rebuilding changed code later, choose a new tag and update the manifests so existing pods cannot quietly keep an older cached image.

## Load the images into the node

Your container runtime's image store and Kind's Kubernetes node image store are separate. Building an image on the host does not make it available to a Pod.

For Podman, create a temporary directory and export each image as a Docker-format archive:

```sh
PULSE_BOOK_ARCHIVES=$(mktemp -d)
podman save --format docker-archive -o "$PULSE_BOOK_ARCHIVES/controller.tar" localhost/pulse-controller:book-v1
kind load image-archive "$PULSE_BOOK_ARCHIVES/controller.tar" --name pulse-book
podman save --format docker-archive -o "$PULSE_BOOK_ARCHIVES/runner.tar" localhost/pulse-probe-runner:book-v1
kind load image-archive "$PULSE_BOOK_ARCHIVES/runner.tar" --name pulse-book
podman save --format docker-archive -o "$PULSE_BOOK_ARCHIVES/target.tar" localhost/pulse-demo-target:book-v1
kind load image-archive "$PULSE_BOOK_ARCHIVES/target.tar" --name pulse-book
```

Keep the provider selected as in the environment chapter. Record the temporary directory for later removal; these archives are disposable copies of the built images.

Docker users load directly:

```sh
kind load docker-image localhost/pulse-controller:book-v1 --name pulse-book
kind load docker-image localhost/pulse-probe-runner:book-v1 --name pulse-book
kind load docker-image localhost/pulse-demo-target:book-v1 --name pulse-book
```

## Teach the API server the resource types

Inspect `api/v1alpha1/` and `config/crd/bases/`. The Go types and markers define schemas; the YAML is generated output. For this pinned source revision, install the checked-in schemas directly:

```sh
kubectl --context kind-pulse-book apply -f config/crd/bases/
kubectl --context kind-pulse-book wait --for=condition=Established --timeout=60s \
  crd/httpcanaries.canary.iambarton.com \
  crd/grpccanaries.canary.iambarton.com \
  crd/anomalypolicies.canary.iambarton.com
kubectl --context kind-pulse-book explain httpcanary.spec
```

Expect three Established CRDs and the schema for an HTTP canary. CRD installation alone creates no checks. Schema changes during development must be regenerated from Go markers; do not fix an API design by editing generated YAML.

## Inspect and render the installation

Open `book/examples/install/kustomization.yaml`. Its resources reference the standard installation; its image substitution selects the controller you loaded. The Deployment patch tells the controller which runner and engine images to use for the workloads it manages. The engine reference is reserved for the later model chapter and is not pulled while no canary enables intelligence.

Kustomize renders manifests; it does not deploy them. Render to a temporary file and inspect it:

```sh
PULSE_BOOK_INSTALL=$(mktemp)
kubectl kustomize book/examples/install > "$PULSE_BOOK_INSTALL"
less "$PULSE_BOOK_INSTALL"
```

Find these pieces before applying:

| Resource | Why it exists |
| --- | --- |
| `pulse-system` namespace | Contains shared operator infrastructure |
| Manager ServiceAccount | Gives the controller a Kubernetes identity |
| ClusterRole and binding | Let reconciliation read canaries and manage shared resources |
| Leader-election Role and binding | Coordinate which manager leads |
| Controller Deployment | Runs the manager with configured image references and readiness checks |
| Metrics Service and policy | Expose authenticated metrics and limit allowed network sources |
| CRDs | The same schemas installed above; reapplication is idempotent |

The default installation also includes metrics authorization roles and network policies. Inspect their selectors and allowed sources; successful authentication alone does not bypass a NetworkPolicy. This lab does not claim that all generated runtime pods satisfy a namespace with restricted Pod Security enforcement—verify that separately before adopting such a policy.

Apply the reviewed output and wait for the manager:

```sh
kubectl --context kind-pulse-book apply -f "$PULSE_BOOK_INSTALL"
kubectl --context kind-pulse-book -n pulse-system rollout status deployment/pulse-controller-manager --timeout=180s
kubectl --context kind-pulse-book -n pulse-system logs deployment/pulse-controller-manager --tail=40
kubectl --context kind-pulse-book -n pulse-system get deploy,sts,svc,configmap
```

Expect a Ready manager. It may reconcile an empty runner configuration before you create a canary. The optional incident engine should be absent. Initial logs can mention unavailable results while the runner starts; repeatedly missing results after readiness require investigation.

If the rollout fails, inspect `kubectl --context kind-pulse-book -n pulse-system get pods` and the affected Pod's events. `ImagePullBackOff` usually means the manifest reference does not match an image loaded into this node. An API `Forbidden` error points to permissions rather than image loading.

Checkpoint: identify which image reference starts the controller and which environment setting controls its generated runner. Explain why changing your host image tag alone does not update an existing Pod.
