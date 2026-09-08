# Install the intelligence runtime

## Objective

Build the native ONNX incident engine, load it into the isolated Kind node, deploy the local recording sink and complete fixture set, and prove that both embedded models loaded. This adds intelligence to the deterministic installation; it does not replace protocol assertions.

## Prerequisites and starting state

Complete the policy, Potion, and latency chapters on `book-shop/catalogue` first. Those exercises opt one canary into intelligence and may already have loaded `localhost/pulse-incident-engine:book-v1`. This chapter adds a second lab namespace, `shop`, with the multi-service fixture set used by later incident, novelty, and action experiments. It does not replace `book-shop`. Keep `kind-pulse-book` explicit. These checks must pass:

```sh
kubectl --context kind-pulse-book wait --for=condition=Ready node --all --timeout=120s
kubectl --context kind-pulse-book -n pulse-system rollout status \
  deployment/pulse-controller-manager --timeout=180s
test -s hack/models/potion/model.bin
test -s hack/models/minilm/model.onnx
```

The controller must already have `PULSE_INCIDENT_ENGINE_IMAGE=localhost/pulse-incident-engine:book-v1`, as rendered by `book/examples/install/kustomization.yaml`. If `kubectl --context kind-pulse-book -n pulse-system get deployment pulse-incident-engine` already succeeds, the engine is present because `book-shop/catalogue` is still opted in; later shop canaries join that same process.

## Build and load the engine

The incident-engine Dockerfile downloads ONNX Runtime 1.22.0 for `amd64` or `arm64`, builds Go with cgo and the `onnx` tag, and copies MiniLM plus the native shared library into a non-root distroless image.

With Podman:

```sh
podman build -f Dockerfile.incidentengine \
  -t localhost/pulse-incident-engine:book-v1 .
PULSE_ENGINE_ARCHIVE=$(mktemp)
podman save --format docker-archive \
  -o "$PULSE_ENGINE_ARCHIVE" localhost/pulse-incident-engine:book-v1
kind load image-archive "$PULSE_ENGINE_ARCHIVE" --name pulse-book
```

With Docker:

```sh
docker build -f Dockerfile.incidentengine \
  -t localhost/pulse-incident-engine:book-v1 .
kind load docker-image localhost/pulse-incident-engine:book-v1 --name pulse-book
```

An unsupported architecture fails during the ONNX Runtime download stage instead of silently building a model-free engine. Skip the build and load if `podman image exists localhost/pulse-incident-engine:book-v1` or `docker image inspect localhost/pulse-incident-engine:book-v1` already succeeds from the policy chapter.

## Deploy inspectable fixtures

The checked-in target manifest uses one image placeholder. Render only that substitution, inspect the result, then apply it:

```sh
PULSE_BOOK_TARGETS=$(mktemp)
sed 's|PULSE_DEMO_TARGET_IMAGE|localhost/pulse-demo-target:book-v1|g' \
  hack/demo/00-targets.yaml > "$PULSE_BOOK_TARGETS"
less "$PULSE_BOOK_TARGETS"
kubectl --context kind-pulse-book apply -f "$PULSE_BOOK_TARGETS"
kubectl --context kind-pulse-book -n shop wait --for=condition=Available \
  deployment --all --timeout=180s
```

The `shop` namespace now has catalogue, two real catalogue callers, an unrelated HTTP control, an MCP server, and a gRPC health server. Leave `book-shop` in place; later recovery commands name both namespaces. The `__control` routes mutate fixture behavior in place. They simulate a changed release response; they are not a blue/green Kubernetes rollout.

The sink image is `python:3.12-alpine` and is not one of the local `book-v1` images. Load it into the Kind node before applying the manifest, or the sink Pod stays in `ImagePullBackOff` on an offline host.

With Podman:

```sh
podman pull docker.io/library/python:3.12-alpine
podman tag docker.io/library/python:3.12-alpine python:3.12-alpine
PULSE_SINK_ARCHIVE=$(mktemp)
podman save --format docker-archive -o "$PULSE_SINK_ARCHIVE" python:3.12-alpine
kind load image-archive "$PULSE_SINK_ARCHIVE" --name pulse-book
```

With Docker:

```sh
docker pull python:3.12-alpine
kind load docker-image python:3.12-alpine --name pulse-book
```

If `kind load docker-image` fails with `ctr: ... content digest ... not found`, Kind's `--all-platforms` import is choking on the image's attestation manifests. Import the saved archive without that flag:

```sh
docker save python:3.12-alpine | \
  docker exec -i pulse-book-control-plane ctr --namespace=k8s.io images import -
```

Deploy the local recording sink:

```sh
kubectl --context kind-pulse-book apply -f hack/demo/05-sink.yaml
kubectl --context kind-pulse-book -n pulse-demo rollout status \
  deployment/sink --timeout=180s
```

The sink stands in for Slack, an OpenAI-compatible investigation endpoint, and Datadog intake. Its generated investigation is deterministic fixture text, not external generative inference. Its Secret values are lab-only; never copy real credentials into published commands or logs.

## Apply the policy and canaries

Read both declarations before applying them:

```sh
less hack/demo/10-policy.yaml
less hack/demo/20-canaries.yaml
kubectl --context kind-pulse-book apply -f hack/demo/10-policy.yaml
kubectl --context kind-pulse-book apply -f hack/demo/20-canaries.yaml
```

The controller sees `spec.intelligence`, renders effective hot/cold model settings, and creates the optional engine. Wait for each runtime:

```sh
kubectl --context kind-pulse-book -n pulse-system rollout status \
  statefulset/pulse-probe-runner --timeout=180s
kubectl --context kind-pulse-book -n pulse-system rollout status \
  deployment/pulse-incident-engine --timeout=180s
kubectl --context kind-pulse-book -n pulse-system get deploy,sts,pods,svc
```

If the engine reports `ImagePullBackOff`, compare its image reference with the image loaded into the node. A crash mentioning the ONNX API or shared library is a native runtime mismatch, not evidence that MiniLM fell back successfully.

## Prove both models loaded

Read process logs without exposing model inputs or credentials:

```sh
kubectl --context kind-pulse-book -n pulse-system logs \
  statefulset/pulse-probe-runner --since=10m |
  grep 'Loaded the body-drift model'
kubectl --context kind-pulse-book -n pulse-system logs \
  deployment/pulse-incident-engine --since=10m |
  grep 'Loaded the failure-path model'
```

Both lines are required. `Could not load ... model` means the corresponding capability is disabled even if the Pod is Ready.

Require all ten fixture canaries to reach a deterministic healthy baseline:

```sh
for canary in catalogue checkout search no-content login-journey mcp-tools \
  similar-a similar-b unrelated; do
  kubectl --context kind-pulse-book -n shop wait httpcanary/"$canary" \
    --for=jsonpath='{.status.phase}'=Healthy --timeout=180s
done
kubectl --context kind-pulse-book -n shop wait grpccanary/orders \
  --for=jsonpath='{.status.phase}'=Healthy --timeout=180s
```

Inspect the complete live view through the API-server Service proxy:

```sh
kubectl --context kind-pulse-book -n pulse-system get --raw \
  '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/results' |
  python3 -m json.tool
kubectl --context kind-pulse-book -n pulse-system get --raw \
  '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/incidents' |
  python3 -m json.tool
```

Timestamps, durations, and ordering vary. The baseline should contain ten current results and no open incident.

## Bounded recovery

Inspect engine events and model paths before rebuilding:

```sh
kubectl --context kind-pulse-book -n pulse-system describe \
  deployment/pulse-incident-engine
kubectl --context kind-pulse-book -n pulse-system logs \
  deployment/pulse-incident-engine --tail=100
kubectl --context kind-pulse-book -n pulse-system get configmap \
  pulse-probe-config -o yaml
```

After correcting an image or model problem, restart and bound the wait:

```sh
kubectl --context kind-pulse-book -n pulse-system rollout restart \
  deployment/pulse-incident-engine
kubectl --context kind-pulse-book -n pulse-system rollout status \
  deployment/pulse-incident-engine --timeout=180s
```

Keep these resources for subsequent experiments. Remove only disposable rendered files and archives when no process is using them:

```sh
rm -f "$PULSE_BOOK_TARGETS" "${PULSE_ENGINE_ARCHIVE:-}" "${PULSE_SINK_ARCHIVE:-}"
```

Checkpoint: identify which evidence proves MiniLM loaded, which evidence only proves the process is Ready, and why a healthy deterministic baseline does not by itself prove that Potion has enough samples to detect drift.
