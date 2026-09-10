# Detect passing-body drift with Potion

Before running operational API examples, complete [authenticated operational API access](../operational-api-access.md) and keep that port-forward active.

## Learning objective

Explain Potion's hot-path embedding and observe a response that still passes its HTTP assertions but crosses a learned semantic-distance threshold.

> Runtime verification required: no command or expected value in this chapter was executed while authoring it. Record the revision, measured score, and sample count when running the lab.

## Prerequisites and starting state

Use `kind-pulse-book` with the prepared 512-dimensional Potion artifacts baked into `localhost/pulse-probe-runner:book-v1`. Reapply `/tmp/pulse-book-policy.yaml` and the canary opt-in from the policy chapter. For a short lab, set only this policy's warmup and debounce:

```sh
kubectl --context kind-pulse-book -n book-shop patch anomalypolicy book-triage --type merge -p '
{"spec":{"triggers":{"bodyDrift":{
  "enabled":true,
  "threshold":"0.15",
  "warmupChecks":5,
  "consecutiveBreaches":2,
  "maxBodyBytes":4096,
  "sampleEvery":1
}}}}'
kubectl --context kind-pulse-book -n book-shop patch httpcanary catalogue --type merge -p '
{"spec":{"intelligence":{
  "enabled":true,
  "policyRef":{"name":"book-triage"},
  "overrides":{"driftThreshold":"0.15","warmupChecks":5}
}}}'
```

Confirm the target's deterministic contract still requires HTTP 200 and the literal marker `items`:

```sh
kubectl --context kind-pulse-book -n book-shop get httpcanary catalogue \
  -o jsonpath='{.spec.expectedStatus}{" "}{.spec.containsText}{"\n"}'
kubectl --context kind-pulse-book get --raw \
  '/api/v1/namespaces/book-shop/services/http:catalogue:8080/proxy/__control'
```

The control response should be `{"behavior":"healthy"}`.

## Understand what is learned

The runner normalizes each passing body by applying policy redactions, masking timestamps, UUIDs, IP addresses, durations, long hexadecimal values, and numbers, then lowercasing and collapsing whitespace. It truncates to `maxBodyBytes`, tokenizes with WordPiece, looks up static token vectors, averages them, and L2-normalizes a 512-value Potion vector.

Each canary owns an in-memory centroid. During warmup the runner absorbs samples but does not trust a score. After warmup it computes cosine distance from the centroid. A value strictly greater than the threshold increments the consecutive-breach counter and is not absorbed, preventing a sustained bad body from training itself into normal. Passing, below-threshold values update the centroid with an EWMA-like running weight.

The raw body and centroid remain in the runner. `/results` exposes only state, sample count, and score.

## Wait for a real baseline

```sh
for attempt in $(seq 1 60); do
  RESULT=$(curl --fail --max-time 5 -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" http://127.0.0.1:19091/results)
  STATE=$(printf '%s' "$RESULT" | python3 -c '
import json,sys
for result in json.load(sys.stdin):
    if result["name"] == "book-shop/catalogue":
        print(result.get("driftState", ""))
        break')
  [ "$STATE" = ready ] && break
  sleep 2
done
[ "$STATE" = ready ]
printf '%s' "$RESULT" | python3 -m json.tool
```

The bounded loop must end with `driftState: ready` and at least five `driftSamples`. Do not use the CR's unchanged healthy timestamp as proof of fresh sampling; use the engine's live aggregate.

A runner restart drops the in-memory centroid. The engine's `/results` snapshot can still say `ready` until a new observation arrives. If you just restarted the runner, wait until `driftState` becomes `warming` (or the sample count resets) before treating `ready` as a new healthy baseline. Mutating the fixture during warmup teaches the changed body as normal and will not raise `bodyDrift`.

## Introduce a green semantic change

The fixture's `green` behavior changes two products into an empty list while retaining HTTP 200 and the word `items`.

```sh
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/book-shop/services/http:catalogue:8080/proxy/__control' -f -
{"behavior":"green"}
EOF
kubectl --context kind-pulse-book get --raw \
  '/api/v1/namespaces/book-shop/services/http:catalogue:8080/proxy/'
```

Independently expect `{"items":[],"total":0}`. This is in-place fixture mutation that simulates changed application behavior; it is not a Kubernetes blue/green rollout.

Wait for the debounced signal:

```sh
kubectl --context kind-pulse-book -n book-shop wait httpcanary/catalogue \
  --for=jsonpath='{.status.intelligence.trigger}'=bodyDrift --timeout=180s
kubectl --context kind-pulse-book -n book-shop get httpcanary catalogue -o yaml
curl --fail --max-time 5 -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" http://127.0.0.1:19091/results |
  python3 -m json.tool
curl --fail --max-time 5 -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" http://127.0.0.1:19091/incidents |
  python3 -m json.tool
```

Expected evidence, requiring later runtime verification: the canary remains `Healthy`; the live result has `driftState: ready`, a numeric `driftScore` greater than `0.15`, and a stable baseline sample count while breached; status reports `trigger: bodyDrift` and the same score rounded to four decimals; a single-member incident names `book-shop/catalogue` as root. Record the actual score—do not copy a fixture comment as measured evidence.

Potion contributes the vector and cosine distance. The HTTP pass, warmup count, threshold comparison, consecutive-breach debounce, incident creation, and status projection are deterministic.

## Failure symptoms and bounded recovery

If `driftState` is absent, inspect model loading and the flattened trigger. If it remains `warming`, check that live `lastCheckTime` advances. If the score moves but no incident opens, verify two consecutive breaches.

```sh
kubectl --context kind-pulse-book -n pulse-system logs statefulset/pulse-probe-runner \
  --all-pods --tail=150
kubectl --context kind-pulse-book -n pulse-system get configmap pulse-probe-config \
  -o jsonpath='{.data.probes\.yaml}' | sed -n '/book-shop\/catalogue/,+35p'
```

Restore the fixture and wait for the passing body to clear the signal:

```sh
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/book-shop/services/http:catalogue:8080/proxy/__control' -f -
{"behavior":"healthy"}
EOF
for attempt in $(seq 1 90); do
  ID=$(kubectl --context kind-pulse-book -n book-shop get httpcanary catalogue \
    -o jsonpath='{.status.intelligence.incidentID}')
  [ -z "$ID" ] && break
  sleep 2
done
[ -z "$ID" ]
```

A runner restart discards the baseline and requires warmup again.

## Checkpoint

Why is this response both deterministically healthy and model-signalled? Explain why breached samples are not absorbed, why two model spaces cannot share a baseline, and which fields prove that a real baseline—not an illustrative score—was used.
