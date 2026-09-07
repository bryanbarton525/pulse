# Detect latency shifts with EWMA statistics

## Learning objective

Observe a passing endpoint become slow, calculate what Pulse compares, and separate per-canary EWMA statistics from every embedding-based feature.

> Runtime verification required: run this experiment on `kind-pulse-book` and record the actual z-scores. The expected evidence below is not an execution claim.

## Prerequisites and starting state

Start with the healthy catalogue and `book-triage` policy. Remove the shared warmup override from the canary so latency can use the policy value, then set a short fixture-specific baseline:

```sh
kubectl --context kind-pulse-book -n book-shop patch httpcanary catalogue \
  --type json -p='[{"op":"remove","path":"/spec/intelligence/overrides"}]'
kubectl --context kind-pulse-book -n book-shop patch anomalypolicy book-triage --type merge -p '
{"spec":{"triggers":{"latencyShift":{
  "enabled":true,
  "zScoreThreshold":"3.0",
  "warmupChecks":8,
  "consecutiveBreaches":3
}}}}'
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/book-shop/services/http:catalogue:8080/proxy/__control' -f -
{"behavior":"healthy"}
EOF
```

This short warmup is for the deterministic fixture. The API default is 30 checks.

## Why the detector works

Latency is measured around the complete check and is evaluated only when that check passes. During warmup, every duration updates an exponentially weighted mean and variance. Thereafter:

```text
deviation = max(sqrt(variance), abs(mean) * 0.05, 0.001 seconds)
z-score   = (duration - mean) / deviation
```

The floor makes a perfectly stable endpoint observable instead of dividing by zero. A value strictly above the configured z-score threshold is a breach. Three consecutive breaches are required here. Breaches are not absorbed; below-threshold passing checks reset the consecutive count and update the baseline. Failing checks never enter the latency baseline because timeouts would teach the client timeout as normal.

Every canary has independent state. A slow upstream can therefore raise separate latency incidents on real downstream callers, but latency does not correlate them.

## Wait for readiness and inspect the baseline output

```sh
for attempt in $(seq 1 90); do
  RESULTS=$(kubectl --context kind-pulse-book -n pulse-system get --raw \
    '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/results')
  STATE=$(printf '%s' "$RESULTS" | python3 -c '
import json,sys
for result in json.load(sys.stdin):
    if result["name"] == "book-shop/catalogue":
        print(result.get("latencyState", ""))
        break')
  [ "$STATE" = ready ] && break
  sleep 2
done
[ "$STATE" = ready ]
printf '%s' "$RESULTS" | python3 -c '
import json,sys
for result in json.load(sys.stdin):
    if result["name"] == "book-shop/catalogue":
        print(json.dumps(result, indent=2))'
```

Expect `latencyState: ready`, `latencySamples` of at least eight, and a finite `latencyZScore`. A near-zero score is ordinary; it does not mean the detector was skipped.

## Add two seconds without breaking the contract

```sh
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/book-shop/services/http:catalogue:8080/proxy/__control' -f -
{"behavior":"slow"}
EOF
kubectl --context kind-pulse-book get --raw \
  '/api/v1/namespaces/book-shop/services/http:catalogue:8080/proxy/'
kubectl --context kind-pulse-book -n book-shop wait httpcanary/catalogue \
  --for=jsonpath='{.status.intelligence.trigger}'=latencyShift --timeout=240s
kubectl --context kind-pulse-book -n book-shop get httpcanary catalogue \
  -o jsonpath='{.status.phase}{" "}{.status.intelligence.score}{" "}{.status.intelligence.incidentID}{"\n"}'
kubectl --context kind-pulse-book -n pulse-system get --raw \
  '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/results' |
  python3 -m json.tool
```

Expected evidence, requiring later runtime verification: the direct request takes about two seconds and returns the normal body; phase remains `Healthy`; three successive above-3.0 z-scores cause `latencyShift`; live results retain at least eight baseline samples; status rounds the signal score to four decimals.

No model contributes to this decision. Duration measurement, EWMA mean and variance, deviation floor, threshold, debounce, and one-probe incident are deterministic.

To demonstrate independent baselines, deploy the current checkout and search fixtures and canaries from `hack/demo/00-targets.yaml` and `hack/demo/20-canaries.yaml` under their declared `shop` namespace, or author equivalent callers. Do not infer propagation from the catalogue alone: independently curl each caller and inspect each caller's own z-score and incident ID.

## Failure symptoms and bounded recovery

If the request exceeds ten seconds, the runner reports an HTTP failure and latency evaluation does not run. If the score never breaches, verify that the policy reload reached the runner and that the fixture says `slow`.

```sh
kubectl --context kind-pulse-book get --raw \
  '/api/v1/namespaces/book-shop/services/http:catalogue:8080/proxy/__control'
kubectl --context kind-pulse-book -n pulse-system logs statefulset/pulse-probe-runner \
  --all-pods --tail=120
kubectl --context kind-pulse-book -n pulse-system get configmap pulse-probe-config \
  -o jsonpath='{.data.probes\.yaml}' | sed -n '/book-shop\/catalogue/,+35p'
```

Restore and wait for a new live result with a cleared incident:

```sh
BEFORE=$(kubectl --context kind-pulse-book -n pulse-system get --raw \
  '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/results' |
  python3 -c '
import json,sys
print(next(r["lastCheckTime"] for r in json.load(sys.stdin)
           if r["name"] == "book-shop/catalogue"))')
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/book-shop/services/http:catalogue:8080/proxy/__control' -f -
{"behavior":"healthy"}
EOF
for attempt in $(seq 1 90); do
  ID=$(kubectl --context kind-pulse-book -n book-shop get httpcanary catalogue \
    -o jsonpath='{.status.intelligence.incidentID}')
  NOW=$(kubectl --context kind-pulse-book -n pulse-system get --raw \
    '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/results' |
    python3 -c '
import json,sys
print(next(r["lastCheckTime"] for r in json.load(sys.stdin)
           if r["name"] == "book-shop/catalogue"))')
  [ -z "$ID" ] && [ "$NOW" != "$BEFORE" ] && break
  sleep 2
done
[ -z "$ID" ] && [ "$NOW" != "$BEFORE" ]
```

## Checkpoint

Why can a stable service with zero measured variance still produce a finite z-score? Why are timeout failures excluded? If three callers slow down, why are their latency baselines and incidents independent even when one call path caused all three?
