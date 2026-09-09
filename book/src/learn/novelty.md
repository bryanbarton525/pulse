# Replay a failure and classify novelty

## Learning objective

Recover and replay the same outage, compare incident IDs and novelty state, and explain the in-memory cluster index, settling period, action cooldown, and hourly rate limit.

> Runtime verification required: this replay has not been run while authoring the chapter. Save both incident documents and metric snapshots when validating it.

## Prerequisites and starting state

Continue with the `shop` fixtures from the incidents chapter. All canaries must be healthy and `/incidents` must be `[]`. Use the strict demo replay threshold and short settling period:

```sh
kubectl --context kind-pulse-book -n pulse-system patch anomalypolicy demo-triage --type merge -p '
{"spec":{
  "triggers":{"failureNovelty":{
    "enabled":true,
    "clusterThreshold":"0.999",
    "settlingPeriodSeconds":1
  }},
  "throttle":{"cooldownSeconds":0,"maxPerHour":50}
}}'
kubectl --context kind-pulse-book -n pulse-system rollout restart \
  deployment/pulse-incident-engine
kubectl --context kind-pulse-book -n pulse-system rollout status \
  deployment/pulse-incident-engine --timeout=180s
sleep 2
kubectl --context kind-pulse-book -n pulse-system get --raw \
  '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/incidents'
```

Restart is deliberate here: it clears the engine's in-memory failure clusters, incidents, inference state, action throttle history, and aggregated result history. Runners continue probing and reassert an ongoing failure at most every two minutes, so begin from a recovered state.

## Capture the first occurrence

```sh
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:catalogue:8080/proxy/__control' -f -
{"behavior":"outage"}
EOF
kubectl --context kind-pulse-book -n shop wait httpcanary/catalogue \
  --for=jsonpath='{.status.intelligence.novel}'=true --timeout=180s
FIRST_ID=$(kubectl --context kind-pulse-book -n shop get httpcanary catalogue \
  -o jsonpath='{.status.intelligence.incidentID}')
[ -n "$FIRST_ID" ]
kubectl --context kind-pulse-book -n pulse-system get --raw \
  '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/incidents' \
  > /tmp/pulse-book-first-incident.json
python3 -m json.tool /tmp/pulse-book-first-incident.json
kubectl --context kind-pulse-book -n pulse-system get --raw \
  '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/metrics' \
  > /tmp/pulse-book-metrics-first.txt
```

After the five-second dispatch debounce, the engine embeds the root failure text. With no cluster above similarity `0.999`, it creates a cluster and reports `novel: true`, provided the one-second startup settling period has elapsed. Novelty does not detect the outage; deterministic canary assertions already did that.

## Recover completely

```sh
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:catalogue:8080/proxy/__control' -f -
{"behavior":"healthy"}
EOF
for canary in catalogue checkout search; do
  kubectl --context kind-pulse-book -n shop wait httpcanary/"$canary" \
    --for=jsonpath='{.status.phase}'=Healthy --timeout=180s
done
for attempt in $(seq 1 90); do
  OPEN=$(kubectl --context kind-pulse-book -n pulse-system get --raw \
    '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/incidents')
  [ "$OPEN" = '[]' ] && break
  sleep 2
done
[ "$OPEN" = '[]' ]
```

The recovery observation removes each member; the incident closes when none remain. The failure cluster remains.

## Replay the identical shape

```sh
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:catalogue:8080/proxy/__control' -f -
{"behavior":"outage"}
EOF
kubectl --context kind-pulse-book -n shop wait httpcanary/catalogue \
  --for=jsonpath='{.status.intelligence.novel}'=false --timeout=180s
SECOND_ID=$(kubectl --context kind-pulse-book -n shop get httpcanary catalogue \
  -o jsonpath='{.status.intelligence.incidentID}')
[ -n "$SECOND_ID" ] && [ "$SECOND_ID" != "$FIRST_ID" ]
kubectl --context kind-pulse-book -n pulse-system get --raw \
  '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/incidents' \
  > /tmp/pulse-book-second-incident.json
python3 -m json.tool /tmp/pulse-book-second-incident.json
kubectl --context kind-pulse-book -n pulse-system get --raw \
  '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/metrics' \
  > /tmp/pulse-book-metrics-second.txt
diff -u /tmp/pulse-book-metrics-first.txt /tmp/pulse-book-metrics-second.txt || true
```

Expected evidence, requiring later verification:

- the second opening has a different time-based incident ID;
- root text joins the remembered cluster at similarity at least `0.999`;
- status and incident show `novel: false`;
- `pulse_incidents_total{trigger="failureCorrelation",novel="false"}` and action-attempt metrics increase.

MiniLM contributes the vector used for nearest-cluster similarity. Cluster lookup, threshold, occurrence count, new ID, incident ID, and routing decision are deterministic.

## Understand action routing and rates

Novelty is evaluated only for failure-correlation incidents when a non-empty cold vector exists. It is not evaluated for body drift or latency, for reassertions after engine restart, or when the cold model is absent/failing. `novel` is then omitted rather than false.

A familiar failure skips only an `llm` action. Metric, Slack, and observability actions still run unless throttled. Throttling is keyed by incident signature plus action name, not the new incident ID:

- `cooldownSeconds` blocks the same shape/action until the cooldown expires;
- `maxPerHour` blocks it after the configured count in the rolling hour;
- zero disables that particular limit;
- unchanged config reloads preserve throttle history;
- editing throttle values or restarting the engine resets it.

The API defaults are a 900-second cooldown and four per hour. This lab uses zero and 50 so the replay remains visible. Exact external action counts are inspected in the actions chapter.

## Failure symptoms and bounded recovery

If the first event reports `novel: false`, the engine may still be settling or may already know the shape. Restart from healthy, wait past settling, and repeat. If novelty is absent, require the model-loaded log and inspect embedding errors. Do not reinterpret absent as known.

Restore the target and verify closure:

```sh
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:catalogue:8080/proxy/__control' -f -
{"behavior":"healthy"}
EOF
for canary in catalogue checkout search; do
  kubectl --context kind-pulse-book -n shop wait httpcanary/"$canary" \
    --for=jsonpath='{.status.phase}'=Healthy --timeout=180s
done
for attempt in $(seq 1 90); do
  OPEN=$(kubectl --context kind-pulse-book -n pulse-system get --raw \
    '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/incidents')
  [ "$OPEN" = '[]' ] && break
  sleep 2
done
[ "$OPEN" = '[]' ]
```

## Checkpoint

Why does a replay receive a new incident ID but `novel: false`? Name four cases where novelty is not evaluated. Which actions does familiarity suppress, and which two independent throttle limits can suppress any action?
