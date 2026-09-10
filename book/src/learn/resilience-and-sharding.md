# Degrade safely and shard probe ownership

Before running operational API examples, complete [authenticated operational API access](../operational-api-access.md) and keep that port-forward active.

## Learning objective

Test unavailable models, stale results, runner and engine restarts, and two runner shards. Identify which deterministic behavior survives and which in-memory intelligence state is lost.

> Runtime verification required: these disruptive commands were not executed during authoring. Run them only in the isolated `kind-pulse-book` cluster and save before/after evidence.

## Prerequisites and starting state

Use the healthy `shop` fixtures, `demo-triage`, and no open incidents. Record live freshness before changing anything:

```sh
curl --fail --max-time 5 -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" http://127.0.0.1:19091/results \
  > /tmp/pulse-book-results-before.json
python3 -m json.tool /tmp/pulse-book-results-before.json
kubectl --context kind-pulse-book -n pulse-system get statefulset pulse-probe-runner \
  -o jsonpath='{.spec.replicas}{"\n"}'
```

`lastCheckTime` and engine-computed `liveAgeSeconds` are freshness evidence. A CR may retain an old healthy timestamp because unchanged phase/status/message does not cause a status write.

## Make both embedding models unavailable

Write an explicit model override with nonexistent in-image paths:

```sh
kubectl --context kind-pulse-book -n pulse-system patch anomalypolicy demo-triage --type merge -p '
{"spec":{"model":{
  "hot":{
    "backend":"potion",
    "modelPath":"/models/missing/potion.bin",
    "vocabPath":"/models/missing/potion-vocab.txt",
    "maxSequenceLength":256
  },
  "cold":{
    "backend":"onnx",
    "onnx":{
      "modelPath":"/models/missing/minilm.onnx",
      "vocabPath":"/models/missing/minilm-vocab.txt",
      "maxSequenceLength":256
    }
  }
}}}'
for attempt in $(seq 1 60); do
  RUNNER_LOG=$(kubectl --context kind-pulse-book -n pulse-system logs \
    statefulset/pulse-probe-runner --all-pods --since=2m)
  ENGINE_LOG=$(kubectl --context kind-pulse-book -n pulse-system logs \
    deployment/pulse-incident-engine --since=2m)
  printf '%s' "$RUNNER_LOG" | grep -q 'drift scoring is disabled' &&
    printf '%s' "$ENGINE_LOG" | grep -q 'declared topology only' && break
  sleep 2
done
printf '%s\n%s\n' "$RUNNER_LOG" "$ENGINE_LOG"
```

Now make the declared-topology outage:

```sh
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:catalogue:8080/proxy/__control' -f -
{"behavior":"outage"}
EOF
for canary in catalogue checkout search; do
  kubectl --context kind-pulse-book -n shop wait httpcanary/"$canary" \
    --for=jsonpath='{.status.phase}'=Unhealthy --timeout=180s
done
curl --fail --max-time 5 -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" http://127.0.0.1:19091/incidents |
  python3 -m json.tool
```

Expected evidence, requiring later verification: normal HTTP assertions still fail; latency remains available for passing checks; declared edges still merge the three failures and rank catalogue as root; merge evidence is `declaredEdge`; body-drift scores, similarity evidence, and novelty are absent. With no declared edge, missing cold vectors produce separate incidents rather than speculative grouping.

Restore model defaults by removing the override, then recover:

```sh
kubectl --context kind-pulse-book -n pulse-system patch anomalypolicy demo-triage \
  --type json -p='[{"op":"remove","path":"/spec/model"}]'
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:catalogue:8080/proxy/__control' -f -
{"behavior":"healthy"}
EOF
kubectl --context kind-pulse-book -n pulse-system rollout restart \
  statefulset/pulse-probe-runner deployment/pulse-incident-engine
kubectl --context kind-pulse-book -n pulse-system rollout status \
  statefulset/pulse-probe-runner --timeout=180s
kubectl --context kind-pulse-book -n pulse-system rollout status \
  deployment/pulse-incident-engine --timeout=180s
```

## Observe stale and missing results

Delete the sole runner Pod and inspect continuously:

```sh
CR_BEFORE=$(kubectl --context kind-pulse-book -n shop get httpcanary catalogue \
  -o jsonpath='{.status.lastCheckTime}')
kubectl --context kind-pulse-book -n pulse-system delete pod pulse-probe-runner-0
for attempt in $(seq 1 60); do
  curl --fail --max-time 5 -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" http://127.0.0.1:19091/results \
    > /tmp/pulse-book-results-during-restart.json
  kubectl --context kind-pulse-book -n pulse-system get pod pulse-probe-runner-0
  sleep 2
done
```

Stop the loop early with Ctrl-C once the replacement is Ready and fresh times advance. A short restart may leave the aggregator's last shard snapshot visible with increasing `liveAgeSeconds`. After two minutes without a shard report it omits that shard. Missing results do not rewrite CRs to Healthy or Unhealthy; they leave previous status untouched, so consumers must evaluate freshness separately.

Runner restart loses Potion centroids, latency EWMA state, sampling counters, ongoing-failure onset memory, and signal-clearing memory. It immediately restarts checks and warmup. The engine periodically closes unrefreshed drift/latency incidents after five minutes as a backstop.

Engine restart loses open incidents, novelty clusters, inferred topology, action throttle history, and aggregated history/results. Deterministic probes continue. Ongoing failures are reasserted by runners at most every two minutes; reassertions rebuild membership but do not classify novelty, count an onset, or dispatch actions.

## Configure two stable shards

The manager reads `PULSE_PROBE_RUNNER_SHARDS`; the StatefulSet gives stable numeric ordinals. Set two:

```sh
kubectl --context kind-pulse-book -n pulse-system set env \
  deployment/pulse-controller-manager PULSE_PROBE_RUNNER_SHARDS=2
kubectl --context kind-pulse-book -n pulse-system rollout status \
  deployment/pulse-controller-manager --timeout=180s
kubectl --context kind-pulse-book -n pulse-system rollout status \
  statefulset/pulse-probe-runner --timeout=180s
kubectl --context kind-pulse-book -n pulse-system get statefulset pulse-probe-runner \
  -o jsonpath='{.spec.replicas}{" "}{.spec.template.spec.containers[0].env[?(@.name=="PULSE_PROBE_RUNNER_SHARDS")].value}{"\n"}'
for ordinal in 0 1; do
  local_port=$((19100 + ordinal))
  kubectl --context kind-pulse-book -n pulse-system port-forward \
    "pod/pulse-probe-runner-${ordinal}" "${local_port}:9091" \
    >/tmp/pulse-book-shard-${ordinal}-forward.log 2>&1 &
  eval "PULSE_SHARD_${ordinal}_FORWARD_PID=$!"
  for attempt in $(seq 1 30); do
    code=$(curl --silent --output /dev/null --write-out '%{http_code}' --max-time 1 \
      -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" \
      "http://127.0.0.1:${local_port}/results" || true)
    [ "$code" = "200" ] && break
    sleep 0.2
  done
  curl --fail --max-time 5 -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" \
    "http://127.0.0.1:${local_port}/results" \
    > "/tmp/pulse-book-shard-${ordinal}.json"
  python3 -m json.tool "/tmp/pulse-book-shard-${ordinal}.json"
done
kill "$PULSE_SHARD_0_FORWARD_PID" "$PULSE_SHARD_1_FORWARD_PID" 2>/dev/null || true
wait "$PULSE_SHARD_0_FORWARD_PID" "$PULSE_SHARD_1_FORWARD_PID" 2>/dev/null || true
unset PULSE_SHARD_0_FORWARD_PID PULSE_SHARD_1_FORWARD_PID
```

Each probe name is assigned by FNV-1a 32-bit hash modulo two. The two result lists should be disjoint; their sorted union should equal the engine aggregate:

```sh
python3 - <<'PY'
import json
left = {r["name"] for r in json.load(open("/tmp/pulse-book-shard-0.json"))}
right = {r["name"] for r in json.load(open("/tmp/pulse-book-shard-1.json"))}
assert left.isdisjoint(right), left & right
print("\n".join(sorted(left | right)))
PY
curl --fail --max-time 5 -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" http://127.0.0.1:19091/results |
  python3 -m json.tool
kubectl --context kind-pulse-book -n pulse-system get statefulset pulse-probe-runner \
  -o jsonpath='{range .spec.template.spec.containers[0].resources.requests}{@}{"\n"}{end}'
```

Runners push shard-tagged snapshots every five seconds; the single engine aggregates them and correlates observations across shards. The controller can fall back to polling each stable Pod DNS name if the engine is unavailable. Current default requests are 100m CPU and 128Mi memory per runner; this is configuration, not a tested capacity claim. Measure with `kubectl top` only if a metrics server is installed.

## Failure symptoms and bounded recovery

Duplicate names across shard `/results` suggest a pod fell back to shard 0-of-1 because its `POD_NAME` or shard count is wrong. A ClusterIP runner Service returns one arbitrary shard and is not a complete sharded view. Inspect:

```sh
kubectl --context kind-pulse-book -n pulse-system get pods \
  -l app.kubernetes.io/name=pulse-probe-runner -o wide
kubectl --context kind-pulse-book -n pulse-system get statefulset pulse-probe-runner -o yaml
kubectl --context kind-pulse-book -n pulse-system logs statefulset/pulse-probe-runner \
  --all-pods --tail=100
```

Restore one shard:

```sh
kubectl --context kind-pulse-book -n pulse-system set env \
  deployment/pulse-controller-manager PULSE_PROBE_RUNNER_SHARDS=1
kubectl --context kind-pulse-book -n pulse-system rollout status \
  statefulset/pulse-probe-runner --timeout=180s
kubectl --context kind-pulse-book -n pulse-system get statefulset pulse-probe-runner \
  -o jsonpath='{.spec.replicas}{"\n"}'
```

## Checkpoint

List the state lost by each process restart. Why do missing results preserve an old CR status rather than invent a new one? How do you prove shard ownership is disjoint and the aggregate is complete without using the load-balanced runner Service?
