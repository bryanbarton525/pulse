# Correlate failures into incidents

Before running operational API examples, complete [authenticated operational API access](../operational-api-access.md) and keep that port-forward active.

## Learning objective

Prove two independent merge paths—declared topology and MiniLM similarity—then inspect onset ordering, membership, root selection, merge evidence, and topology proposals without treating any heuristic as causation.

> Runtime verification required: these procedures are derived from the current `shop` fixtures and engine APIs. They were not run while this prose was authored. Capture actual IDs, scores, and timestamps.

## Prerequisites and starting state

Use `kind-pulse-book` with all four `book-v1` images loaded and the incident engine already running. Deploy the current deterministic targets by replacing only the fixture image placeholder:

```sh
sed 's|PULSE_DEMO_TARGET_IMAGE|localhost/pulse-demo-target:book-v1|g' \
  hack/demo/00-targets.yaml > /tmp/pulse-book-targets.yaml
kubectl --context kind-pulse-book apply -f /tmp/pulse-book-targets.yaml
for deployment in catalogue checkout search unrelated mcp orders-grpc; do
  kubectl --context kind-pulse-book -n shop rollout status \
    deployment/"$deployment" --timeout=180s
done
```

Write a policy matching the canary fixture's reference but using only a local metric action:

```sh
cat > /tmp/pulse-book-incidents-policy.yaml <<'EOF'
apiVersion: canary.iambarton.com/v1alpha1
kind: AnomalyPolicy
metadata:
  name: demo-triage
  namespace: pulse-system
spec:
  triggers:
    failureCorrelation:
      enabled: true
      windowSeconds: 120
      similarityThreshold: "0.85"
    failureNovelty:
      enabled: true
      clusterThreshold: "0.999"
      settlingPeriodSeconds: 1
  topology:
    dependsOn:
      - canary: shop/checkout
        upstream: [shop/catalogue]
      - canary: shop/search
        upstream: [shop/catalogue]
    inferDependencies: true
    inferMinObservations: 3
    inferMinConfidence: "0.7"
  actions:
    - name: local
      type: metric
  throttle:
    cooldownSeconds: 0
    maxPerHour: 50
EOF
kubectl --context kind-pulse-book apply -f /tmp/pulse-book-incidents-policy.yaml
kubectl --context kind-pulse-book apply -f hack/demo/20-canaries.yaml
kubectl --context kind-pulse-book -n pulse-system rollout status \
  deployment/pulse-incident-engine --timeout=180s
kubectl --context kind-pulse-book -n pulse-system logs deployment/pulse-incident-engine \
  --tail=100
```

Require a log line that the failure-path model loaded. A Ready engine with a model-load error is only topology-capable, not MiniLM evidence.

## Declared topology with a negative control

First inspect active topology:

```sh
curl --fail --max-time 5 -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" http://127.0.0.1:19091/topology |
  python3 -m json.tool
```

`declared` should contain upstream/downstream pairs from `shop/catalogue` to `shop/checkout` and `shop/search`. `proposals` are separate and do not affect merging.

Mutate the upstream and an unrelated control through their explicit fixture APIs:

```sh
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:catalogue:8080/proxy/__control' -f -
{"behavior":"outage"}
EOF
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:unrelated:8080/proxy/__control' -f -
{"behavior":"control-fail"}
EOF
# These fixture responses are 5xx during the outage. kubectl get --raw
# exits nonzero on non-2xx; append || true if you are running under set -e.
kubectl --context kind-pulse-book get --raw \
  '/api/v1/namespaces/shop/services/http:catalogue:8080/proxy/' || true
kubectl --context kind-pulse-book get --raw \
  '/api/v1/namespaces/shop/services/http:checkout:8080/proxy/' || true
kubectl --context kind-pulse-book get --raw \
  '/api/v1/namespaces/shop/services/http:unrelated:8080/proxy/' || true
```

Expect, subject to later verification, catalogue 529, checkout/search 503 because they call catalogue, and unrelated HTTP 200 with `degraded-control`. Wait and inspect:

```sh
for canary in catalogue checkout search unrelated; do
  kubectl --context kind-pulse-book -n shop wait httpcanary/"$canary" \
    --for=jsonpath='{.status.phase}'=Unhealthy --timeout=180s
done
curl --fail --max-time 5 -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" http://127.0.0.1:19091/incidents \
  > /tmp/pulse-book-incidents.json
python3 -m json.tool /tmp/pulse-book-incidents.json
for canary in catalogue checkout search unrelated; do
  kubectl --context kind-pulse-book -n shop get httpcanary "$canary" \
    -o jsonpath='{.metadata.name}{" "}{.status.intelligence.incidentID}{" "}{.status.intelligence.role}{"\n"}'
done
```

Expected evidence: catalogue, checkout, and search share one ID; catalogue is `rootCause`; callers are `downstream`; unrelated has another ID. The three-member incident contains `declaredEdge` evidence. Declared edges merge without consulting MiniLM. Simultaneous failure alone cannot merge the negative control.

Root ranking is deterministic: among members ordered by failure onset, select the first canary with no failing declared upstream. With no usable edge, earliest onset breaks a tie. This is a useful hypothesis, not proof that the selected process caused the outage.

## Similarity-only MiniLM correlation

Restore all four endpoints and require incident closure:

```sh
for service in catalogue unrelated; do
  cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
    "/api/v1/namespaces/shop/services/http:${service}:8080/proxy/__control" -f -
{"behavior":"healthy"}
EOF
done
for canary in catalogue checkout search unrelated; do
  for attempt in $(seq 1 90); do
    ID=$(kubectl --context kind-pulse-book -n shop get httpcanary "$canary" \
      -o jsonpath='{.status.intelligence.incidentID}')
    [ -z "$ID" ] && break
    sleep 2
  done
  [ -z "$ID" ]
done
```

The two `similar-*` canaries have no topology edge and target the same fixture route. Make their required marker disappear while independently changing gRPC health:

```sh
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:unrelated:8080/proxy/__control' -f -
{"behavior":"similarity-fail"}
EOF
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:orders-grpc:8080/proxy/__control' -f -
{"behavior":"grpc-fail"}
EOF
kubectl --context kind-pulse-book get --raw \
  '/api/v1/namespaces/shop/services/http:unrelated:8080/proxy/similar'
```

```sh
for canary in similar-a similar-b; do
  kubectl --context kind-pulse-book -n shop wait httpcanary/"$canary" \
    --for=jsonpath='{.status.phase}'=Unhealthy --timeout=180s
done
kubectl --context kind-pulse-book -n shop wait grpccanary/orders \
  --for=jsonpath='{.status.phase}'=Unhealthy --timeout=180s
curl --fail --max-time 5 -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" http://127.0.0.1:19091/incidents |
  python3 -m json.tool
```

Expected evidence, requiring later runtime verification: `similar-a` and `similar-b` share an incident; its `mergeEvidence.type` is `similarity`; `similarity` is numeric and at least the recorded `threshold` of `0.85`; `orders` has a different incident ID. Because the pair's normalized failure documents are expected to be identical, a score near 1 is plausible, but only the returned evidence is measured proof.

MiniLM contributes WordPiece tokenization, transformer inference, attention-mask mean pooling, L2-normalized 384-dimensional vectors, and cosine similarity. Normalization/redaction, time-window candidate selection, threshold comparison, merging, ID assignment, root ranking, and status are deterministic. MiniLM here performs similarity only; it neither generates text nor establishes causation.

## Inspect onsets, membership, and proposals

```sh
curl --fail --max-time 5 -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" http://127.0.0.1:19091/incidents |
  python3 -c '
import json,sys
for incident in json.load(sys.stdin):
    print(incident["id"], incident["rootCause"], incident["openedAt"])
    for member in incident["members"]:
        print(" ", member["probe"], member["role"], member["signal"]["at"])
    for evidence in incident.get("mergeEvidence", []):
        print(" ", evidence)'
curl --fail --max-time 5 -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" http://127.0.0.1:19091/topology |
  python3 -m json.tool
kubectl --context kind-pulse-book -n pulse-system get anomalypolicy demo-triage -o yaml
```

Inference counts failure onsets, never repeated failing ticks. A proposal needs three observed co-occurrences and confidence of at least 0.7 in this lab. It remains status-only until a person copies it into `spec.topology.dependsOn`; do not expect one run to produce it.

## Failure symptoms and bounded recovery

No similarity evidence plus a model-load error means topology-only fallback. A merged negative control indicates an actual edge, matching normalized text above threshold, or stale evidence that must be inspected—not “timing correlation.” Missing status can lag the engine's `/incidents` view because the controller projects it asynchronously.

Restore fixture state:

```sh
for service in catalogue unrelated; do
  cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
    "/api/v1/namespaces/shop/services/http:${service}:8080/proxy/__control" -f -
{"behavior":"healthy"}
EOF
done
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:orders-grpc:8080/proxy/__control' -f -
{"behavior":"healthy"}
EOF
for canary in catalogue checkout search similar-a similar-b unrelated; do
  kubectl --context kind-pulse-book -n shop wait httpcanary/"$canary" \
    --for=jsonpath='{.status.phase}'=Healthy --timeout=180s
done
kubectl --context kind-pulse-book -n shop wait grpccanary/orders \
  --for=jsonpath='{.status.phase}'=Healthy --timeout=180s
```

## Checkpoint

For each experiment, name the merge evidence, negative control, model contribution, and deterministic decision. Why can a proposal be visible yet inactive? Under what exact tie does onset time select the root?
