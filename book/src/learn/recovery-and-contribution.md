# Recover the lab and validate a real contribution

## Learning objective

Return every fixture and custom resource to a known state, inspect the already-merged HTTP interval bound, apply the same one-hour maximum to `GrpcCanary`, add a regression test, regenerate derived artifacts through their generator, build and load the changed image, replay the admission check, and finally delete only `kind-pulse-book`.

> Runtime verification required: this contributor exercise is an executable procedure, not a claim that the change or tests were run while authoring this chapter. Perform it on a disposable worktree or branch; do not commit unless that is your contribution workflow.

## Prerequisites and starting state

Start at the repository root with the course lab still present. Verify the context name before any deletion:

```sh
kind get clusters
kubectl --context kind-pulse-book cluster-info
git status --short
```

The exercise intentionally edits gRPC API source, creates a test, and regenerates checked-in output. Preserve unrelated work by using a clean worktree. Do not treat the already-merged HTTP interval maximum as unfinished work.

## Recover every mutable target

Set every control endpoint back to `healthy`:

```sh
for service in catalogue checkout search unrelated mcp; do
  cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
    "/api/v1/namespaces/shop/services/http:${service}:8080/proxy/__control" -f -
{"behavior":"healthy"}
EOF
done
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:orders-grpc:8080/proxy/__control' -f -
{"behavior":"healthy"}
EOF
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/book-shop/services/http:catalogue:8080/proxy/__control' -f -
{"behavior":"healthy"}
EOF
```

Require fresh passing results, not only old CR phases:

```sh
for canary in catalogue checkout search no-content login-journey mcp-tools similar-a similar-b unrelated; do
  kubectl --context kind-pulse-book -n shop wait httpcanary/"$canary" \
    --for=jsonpath='{.status.phase}'=Healthy --timeout=180s
done
kubectl --context kind-pulse-book -n shop wait grpccanary/orders \
  --for=jsonpath='{.status.phase}'=Healthy --timeout=180s
kubectl --context kind-pulse-book -n book-shop wait httpcanary/catalogue \
  --for=jsonpath='{.status.phase}'=Healthy --timeout=180s
kubectl --context kind-pulse-book -n pulse-system get --raw \
  '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/results' |
  python3 -c '
import json,sys
results=json.load(sys.stdin)
for result in results:
    print(result["name"], result["healthy"], result["lastCheckTime"], result.get("liveAgeSeconds", 0))
assert results and all(r["healthy"] for r in results)'
```

Delete chapter-owned definitions explicitly, in dependency order:

```sh
kubectl --context kind-pulse-book delete -f hack/demo/20-canaries.yaml \
  --ignore-not-found
kubectl --context kind-pulse-book -n pulse-system delete anomalypolicy demo-triage \
  --ignore-not-found
kubectl --context kind-pulse-book -n book-shop patch httpcanary catalogue \
  --type json -p='[{"op":"remove","path":"/spec/intelligence"}]' || true
kubectl --context kind-pulse-book -n book-shop delete anomalypolicy book-triage \
  --ignore-not-found
kubectl --context kind-pulse-book delete -f hack/demo/05-sink.yaml \
  --ignore-not-found
kubectl --context kind-pulse-book delete -f /tmp/pulse-book-targets.yaml \
  --ignore-not-found
kubectl --context kind-pulse-book -n pulse-system wait \
  --for=delete deployment/pulse-incident-engine --timeout=120s
```

Keep the original `book-shop/catalogue` canary. The admission replay creates a new `GrpcCanary` and does not need a live gRPC target.

## Inspect the already-merged HTTP bound

`HttpCanary.spec.interval` already has `Maximum=3600` on this branch, with a generated CRD maximum and `internal/controller/httpcanary_validation_envtest_test.go`. Read that change as the pattern. Do not re-run a replace that looks for the old unmarked interval; that edit is already present and the assertion would fail.

```sh
grep -n -A4 'Interval is how often' api/v1alpha1/httpcanary_types.go
grep -n -A6 'interval:' config/crd/bases/canary.iambarton.com_httpcanaries.yaml | head -n 20
```

Expect `+kubebuilder:validation:Maximum=3600` and CRD `maximum: 3600`.

## Change one supported validation

Apply the same one-hour upper bound to `GrpcCanary.spec.interval`, which still has only `Minimum=5`. Use Python for an exact, guarded source edit:

```sh
python3 - <<'PY'
from pathlib import Path
path = Path("api/v1alpha1/grpccanary_types.go")
text = path.read_text()
old = '''\t// Interval is the frequency in seconds to run the check.
\t// +kubebuilder:validation:Minimum=5
\t// +kubebuilder:default=30
\t// +optional
\tInterval int `json:"interval,omitempty"`'''
new = '''\t// Interval is the frequency in seconds to run the check.
\t// +kubebuilder:validation:Minimum=5
\t// +kubebuilder:validation:Maximum=3600
\t// +kubebuilder:default=30
\t// +optional
\tInterval int `json:"interval,omitempty"`'''
assert text.count(old) == 1
path.write_text(text.replace(old, new))
PY
```

This is an API admission rule, not runner logic: values above 3600 should be rejected by the generated CRD before reconciliation.

## Add a meaningful envtest regression

Do not overwrite the existing HTTP interval test. Add a sibling file for gRPC:

```sh
cat > internal/controller/grpccanary_validation_envtest_test.go <<'EOF'
package controller

import (
    apierrors "k8s.io/apimachinery/pkg/api/errors"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"

    canaryv1alpha1 "github.com/bryanbarton525/pulse/api/v1alpha1"
)

var _ = Describe("GrpcCanary schema validation", func() {
    It("rejects an interval above one hour", func() {
        canary := &canaryv1alpha1.GrpcCanary{
            ObjectMeta: metav1.ObjectMeta{
                GenerateName: "interval-too-large-",
                Namespace:    "default",
            },
            Spec: canaryv1alpha1.GrpcCanarySpec{
                URL:      "orders.invalid:50051",
                Interval: 3601,
            },
        }

        err := k8sClient.Create(ctx, canary)
        Expect(err).To(HaveOccurred())
        Expect(apierrors.IsInvalid(err)).To(BeTrue(), "expected Invalid, got %v", err)
    })
})
EOF
gofmt -w api/v1alpha1/grpccanary_types.go \
  internal/controller/grpccanary_validation_envtest_test.go
```

## Regenerate and test without wrappers

Generated CRDs and DeepCopy files must come from markers, never hand edits:

```sh
mkdir -p /tmp/pulse-book-tools
GOBIN=/tmp/pulse-book-tools go install \
  sigs.k8s.io/controller-tools/cmd/controller-gen@v0.20.1
/tmp/pulse-book-tools/controller-gen \
  rbac:roleName=manager-role crd webhook paths="./..." \
  output:crd:artifacts:config=config/crd/bases
/tmp/pulse-book-tools/controller-gen \
  object:headerFile="hack/boilerplate.go.txt" paths="./..."
GOBIN=/tmp/pulse-book-tools go install \
  sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.23
KUBEBUILDER_ASSETS=$(/tmp/pulse-book-tools/setup-envtest use 1.35 \
  --bin-dir /tmp/pulse-book-envtest -p path)
KUBEBUILDER_ASSETS="$KUBEBUILDER_ASSETS" \
  go test ./internal/controller -run TestControllers -count=1 -v
go test ./internal/proberunner -count=1
go vet ./...
git diff --check
git diff -- api/v1alpha1/grpccanary_types.go \
  internal/controller/grpccanary_validation_envtest_test.go \
  config/crd/bases/canary.iambarton.com_grpccanaries.yaml
```

Expected evidence, requiring later verification: the envtest case passes because API-server admission returns `Invalid`; the generated gRPC CRD contains `maximum: 3600`; unrelated generated files either remain unchanged or have generator-explainable differences. The existing HTTP interval test should still pass.

## Build, load, and replay manually

Build the changed manager with a new immutable lab tag and load it:

```sh
podman build -f Dockerfile -t localhost/pulse-controller:book-validation-v1 .
PULSE_BOOK_CHANGED_ARCHIVE=$(mktemp)
podman save --format docker-archive -o "$PULSE_BOOK_CHANGED_ARCHIVE" \
  localhost/pulse-controller:book-validation-v1
kind load image-archive "$PULSE_BOOK_CHANGED_ARCHIVE" --name pulse-book
kubectl --context kind-pulse-book apply \
  -f config/crd/bases/canary.iambarton.com_grpccanaries.yaml
kubectl --context kind-pulse-book -n pulse-system set image \
  deployment/pulse-controller-manager \
  manager=localhost/pulse-controller:book-validation-v1
kubectl --context kind-pulse-book -n pulse-system rollout status \
  deployment/pulse-controller-manager --timeout=180s
```

Docker users replace the first four lines with:

```sh
docker build -f Dockerfile -t localhost/pulse-controller:book-validation-v1 .
kind load docker-image localhost/pulse-controller:book-validation-v1 \
  --name pulse-book
```

Replay admission with editable YAML:

```sh
cat > /tmp/pulse-book-invalid-interval.yaml <<'EOF'
apiVersion: canary.iambarton.com/v1alpha1
kind: GrpcCanary
metadata:
  name: invalid-interval
  namespace: book-shop
spec:
  url: orders-grpc.book-shop.svc:50051
  interval: 3601
EOF
if kubectl --context kind-pulse-book apply -f /tmp/pulse-book-invalid-interval.yaml; then
  echo "ERROR: admission accepted interval 3601" >&2
  exit 1
fi
sed 's/interval: 3601/interval: 3600/' \
  /tmp/pulse-book-invalid-interval.yaml > /tmp/pulse-book-valid-interval.yaml
kubectl --context kind-pulse-book apply -f /tmp/pulse-book-valid-interval.yaml
kubectl --context kind-pulse-book -n book-shop get grpccanary invalid-interval \
  -o jsonpath='{.spec.interval}{"\n"}'
kubectl --context kind-pulse-book -n book-shop delete grpccanary invalid-interval
```

The rejected apply should name `spec.interval` and the maximum. The accepted object should print `3600`. Building the manager proves the changed source still compiles; applying the generated CRD is what changes admission behavior.

For a new API or webhook scaffold, follow the repository rule and use the Kubebuilder CLI, for example:

```sh
kubebuilder create api --group canary --version v1alpha1 --kind Example
```

Do not run that example in this exercise because it creates a different API and edits scaffold files.

## Failure symptoms and bounded recovery

If envtest accepts 3601, inspect the generated gRPC CRD loaded by its test environment. If Kind accepts it, confirm the regenerated `grpccanaries` CRD was applied. If the manager keeps the old image, inspect its image ID and pull policy:

```sh
kubectl --context kind-pulse-book -n pulse-system get deployment \
  pulse-controller-manager -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
kubectl --context kind-pulse-book -n pulse-system get pods \
  -l control-plane=controller-manager \
  -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.containerStatuses[0].imageID}{"\n"}{end}'
```

## Delete exactly this lab

Remove temporary files, then delete only the named Kind cluster:

```sh
rm -f /tmp/pulse-book-policy.yaml \
  /tmp/pulse-book-incidents-policy.yaml \
  /tmp/pulse-book-targets.yaml \
  /tmp/pulse-book-first-incident.json \
  /tmp/pulse-book-second-incident.json \
  /tmp/pulse-book-incidents.json \
  /tmp/pulse-book-metrics-first.txt \
  /tmp/pulse-book-metrics-second.txt \
  /tmp/pulse-book-results-before.json \
  /tmp/pulse-book-results-during-restart.json \
  /tmp/pulse-book-shard-0.json \
  /tmp/pulse-book-shard-1.json \
  /tmp/pulse-book-invalid-interval.yaml \
  /tmp/pulse-book-valid-interval.yaml \
  "$PULSE_BOOK_CHANGED_ARCHIVE"
kind delete cluster --name pulse-book
kind get clusters
```

This leaves local container images, downloaded model artifacts under `hack/models`, `/tmp/pulse-book-tools`, `/tmp/pulse-book-envtest`, and your gRPC interval source/test/generated contribution. Review them with `git status --short`. Remove local model artifacts only with the exact reset in the model-preparation chapter; remove or commit source changes according to your contributor workflow.

## Checkpoint

Why does the marker change require CRD regeneration, while the manager image alone cannot enforce admission? Which test proves rejection at the API server? After cleanup, name every class of local artifact that intentionally remains.
