# Create your laboratory

This chapter creates an isolated Kubernetes cluster named `pulse-book`. It does not install Pulse yet. The installation chapter will build on the cluster created here.

You need a working Docker or Podman runtime, Kind, kubectl, Git, Python 3, and Go compatible with the repository's `go.mod`. The gRPC chapter also needs a health client; that chapter installs `grpc-health-probe` or uses a temporary Go program. Check the versions before continuing:

```sh
kind version
kubectl version --client
git --version
python3 --version
go version
```

For Docker, confirm the server responds and select the provider:

```sh
docker info
export KIND_EXPERIMENTAL_PROVIDER=docker
```

For Podman, confirm its machine is running and select its provider instead:

```sh
podman info
export KIND_EXPERIMENTAL_PROVIDER=podman
```

On macOS, Podman runs Linux containers in a virtual machine. A working command-line client alone does not establish that the machine has started. Resolve runtime errors before creating the cluster. The existing Pulse demo was tested with Podman; this new manual chapter's platform validation is tracked separately in `plan.md`.

## Write the cluster configuration

Create a local file named `pulse-book-kind.yaml` with this content:

```yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
```

One node is sufficient to learn resource ownership and run the local examples. It is not a test of multi-node availability. Before creating it, list existing clusters:

```sh
kind get clusters
```

If `pulse-book` already exists, inspect whether it is a laboratory you intend to reuse. Do not delete an unfamiliar cluster to make the command succeed. Create the new cluster and wait for readiness:

```sh
kind create cluster --name pulse-book --config pulse-book-kind.yaml --wait 120s
kubectl --context kind-pulse-book wait --for=condition=Ready node --all --timeout=120s
kubectl --context kind-pulse-book rollout status deployment/coredns -n kube-system --timeout=120s
kubectl --context kind-pulse-book get nodes -o wide
kubectl --context kind-pulse-book get pods -n kube-system
```

Expect one Ready node named `pulse-book-control-plane`. System pod names and addresses vary. DNS and network components should become ready before you install applications. If the wait times out, inspect the cluster creation output and available runtime memory; do not interpret partially started infrastructure as a successful setup.

Kind may change your current kubeconfig context. Commands in this course use `--context kind-pulse-book` explicitly so their destination remains visible.

## Inspect the empty environment

```sh
kubectl --context kind-pulse-book get namespaces
kubectl --context kind-pulse-book get crds
```

On a newly created cluster, there should be no Pulse CRDs and no Pulse workloads. This is your starting checkpoint: the Kubernetes API exists, but it does not yet know `HttpCanary`, `GrpcCanary`, or `AnomalyPolicy`.

Save the versions you used with your laboratory notes. Later chapters will add model artifact identities and image references so another engineer can reproduce your results.

## Stop or continue

Keep the cluster for subsequent chapters. To remove this specific laboratory when finished:

```sh
kind delete cluster --name pulse-book
kind get clusters
```

This removes the named cluster and its Kubernetes data. It does not remove cloned source, downloaded models, or cached container images.

Checkpoint: identify the difference between a container runtime, Kind, and Kubernetes; explain why a Ready node does not establish that Pulse is installed.
