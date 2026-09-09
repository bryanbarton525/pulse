# Trace incident actions into a local recording sink

## Learning objective

Deploy the repository's local sink, configure credentials and ordered actions, and inspect the exact chat prompt, deterministic fixture response, projected investigation, Slack body, observability record, and action metrics.

> Runtime verification required: the sink and action chain below were not run while this chapter was authored. Do not use the illustrative count expectations as proof; count the recorded requests.

## Prerequisites and starting state

Continue with healthy `shop` fixtures and no open incidents. The sink uses `python:3.12-alpine`, so the Kind node must be able to pull that image or you must pre-load the exact image. Inspect its source before deployment:

```sh
sed -n '1,220p' hack/demo/05-sink.yaml
kubectl --context kind-pulse-book apply -f hack/demo/05-sink.yaml
kubectl --context kind-pulse-book -n pulse-demo rollout status \
  deployment/sink --timeout=180s
kubectl --context kind-pulse-book -n pulse-demo get service sink
```

The fixture logs each POST body and selected header presence. Its `/v1/chat/completions` response is selected by string matching and ends with “Deterministic demo response; no real LLM inference was performed.”

## Write credentials and ordered actions

Secret references are resolved in the policy namespace into opaque credential IDs in the mounted auth store. Values do not enter the ConfigMap or canary status.

```sh
cat > /tmp/pulse-book-action-secret.yaml <<'EOF'
apiVersion: v1
kind: Secret
metadata:
  name: book-sink-auth
  namespace: pulse-system
stringData:
  llm-token: book-local-token
  slack-webhook: http://sink.pulse-demo.svc:8080/slack/webhook
  dd-api-key: book-local-datadog-key
EOF
kubectl --context kind-pulse-book apply -f /tmp/pulse-book-action-secret.yaml
kubectl --context kind-pulse-book -n pulse-system patch anomalypolicy demo-triage --type merge -p '
{
  "spec": {
    "actions": [
      {"name":"local","type":"metric"},
      {"name":"investigate","type":"llm","llm":{
        "endpoint":"http://sink.pulse-demo.svc:8080/v1/chat/completions",
        "model":"demo-model",
        "apiKeySecretRef":{"name":"book-sink-auth","key":"llm-token"},
        "contextChecks":20,
        "maxTokens":1024,
        "timeoutSeconds":30
      }},
      {"name":"notify","type":"slack","slack":{
        "webhookSecretRef":{"name":"book-sink-auth","key":"slack-webhook"},
        "includeInvestigation":true
      }},
      {"name":"ship-logs","type":"observability","observability":{
        "provider":"datadog",
        "endpoint":"http://sink.pulse-demo.svc:8080",
        "credentialSecretRef":{"name":"book-sink-auth","key":"dd-api-key"},
        "tags":{"env":"book","team":"sre"}
      }}
    ],
    "throttle":{"cooldownSeconds":0,"maxPerHour":50}
  }
}'
```

Wait for mounted-file propagation and require a successful config reload:

```sh
for attempt in $(seq 1 60); do
  LOGS=$(kubectl --context kind-pulse-book -n pulse-system logs \
    deployment/pulse-incident-engine --since=2m)
  printf '%s' "$LOGS" | grep -q 'Configuration applied' && break
  sleep 2
done
printf '%s\n' "$LOGS"
```

Do not print `pulse-probe-auth`; it contains the resolved values.

## Trigger one isolated failure

Use the unrelated canary so the expected chain belongs to one incident. Restart the healthy engine first to clear prior novelty and throttle state, then wait past the policy's one-second settling period:

```sh
kubectl --context kind-pulse-book -n pulse-system rollout restart \
  deployment/pulse-incident-engine
kubectl --context kind-pulse-book -n pulse-system rollout status \
  deployment/pulse-incident-engine --timeout=180s
sleep 2
BEFORE_LINES=$(kubectl --context kind-pulse-book -n pulse-demo logs \
  deployment/sink --since=24h | wc -l | tr -d ' ')
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:unrelated:8080/proxy/__control' -f -
{"behavior":"control-fail"}
EOF
kubectl --context kind-pulse-book -n shop wait httpcanary/unrelated \
  --for=jsonpath='{.status.phase}'=Unhealthy --timeout=180s
for attempt in $(seq 1 90); do
  INCIDENT=$(kubectl --context kind-pulse-book -n shop get httpcanary unrelated \
    -o jsonpath='{.status.intelligence.incidentID}')
  INVESTIGATION=$(kubectl --context kind-pulse-book -n shop get httpcanary unrelated \
    -o jsonpath='{.status.intelligence.investigation}')
  [ -n "$INCIDENT" ] && [ -n "$INVESTIGATION" ] && break
  sleep 2
done
[ -n "$INCIDENT" ] && [ -n "$INVESTIGATION" ]
```

Actions run serially. The no-op metric action records an attempted-action counter; the chat action fills `Investigation`; the later Slack action can include it; the Datadog action still runs if another sink fails.

## Inspect the actual payloads and counts

```sh
kubectl --context kind-pulse-book -n pulse-demo logs deployment/sink \
  --since=24h > /tmp/pulse-book-sink.log
python3 - "$INCIDENT" /tmp/pulse-book-sink.log <<'PY'
import json, sys
incident = sys.argv[1]
records = []
with open(sys.argv[2]) as stream:
    for line in stream:
        try:
            record = json.loads(line)
        except json.JSONDecodeError:
            continue
        if incident in record.get("body", ""):
            records.append(record)
for record in records:
    print(json.dumps(record, indent=2))
print("counts", {
    "llm": sum(r["path"].startswith("/v1/chat/completions") for r in records),
    "slack": sum(r["path"].startswith("/slack/webhook") for r in records),
    "observability": sum(r["path"].startswith("/api/v2/logs") for r in records),
})
PY
kubectl --context kind-pulse-book -n shop get httpcanary unrelated \
  -o jsonpath='{.status.intelligence.investigation}{"\n"}'
kubectl --context kind-pulse-book -n pulse-system get --raw \
  '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/metrics' |
  grep -E '^pulse_(incidents|incident_members|incident_actions)(_total)?'
```

Expected evidence, requiring later runtime verification:

- exactly one request at each of `/v1/chat/completions`, `/slack/webhook`, and `/api/v2/logs` for this incident;
- chat JSON with model `demo-model`, `stream:false`, `max_tokens:1024`, the built-in bounded SRE system prompt, and a user prompt containing incident ID, root, member, target, observed status/message/time, and bounded recent checks;
- only `authPresent:true` and `authScheme:"Bearer"` in the sink log—not the token;
- Slack JSON shaped as `{"text":"..."}` and containing the deterministic investigation because `llm` precedes `slack`;
- Datadog JSON array with source/service, tags, error status, and nested `pulse` record; `ddkeyPresent:true`;
- one successful attempted-action metric per declared action and an incident/member counter.

The local Potion/MiniLM embeddings used earlier are real model inference when their load evidence exists. The sink's chat text is deterministic fixture logic, not local or external generative inference. The Slack and Datadog endpoints are recorders, not those external services.

## Failure symptoms and bounded recovery

No sink request can mean throttle, config load failure, no novel LLM route, or DNS/network failure. Inspect metrics and engine logs before replaying:

```sh
kubectl --context kind-pulse-book -n pulse-system logs \
  deployment/pulse-incident-engine --tail=200
kubectl --context kind-pulse-book -n pulse-system get events \
  --sort-by=.lastTimestamp | tail -n 30
```

Restore the canary and remove only this chapter's external actions and credentials:

```sh
cat <<'EOF' | kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:unrelated:8080/proxy/__control' -f -
{"behavior":"healthy"}
EOF
kubectl --context kind-pulse-book -n shop wait httpcanary/unrelated \
  --for=jsonpath='{.status.phase}'=Healthy --timeout=180s
kubectl --context kind-pulse-book -n pulse-system patch anomalypolicy demo-triage \
  --type merge -p '{"spec":{"actions":[{"name":"local","type":"metric"}]}}'
kubectl --context kind-pulse-book delete -f /tmp/pulse-book-action-secret.yaml \
  --ignore-not-found
kubectl --context kind-pulse-book delete -f hack/demo/05-sink.yaml \
  --ignore-not-found
rm -f /tmp/pulse-book-action-secret.yaml /tmp/pulse-book-sink.log
```

## Checkpoint

Why must the LLM action precede Slack? Which outbound fields are model-generated in this lab, and which are deterministic? Where are credential values stored, and what evidence proves one attempted action rather than one successful HTTP request?
