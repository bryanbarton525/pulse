# Opt a canary into intelligence

## Learning objective

Create an `AnomalyPolicy`, attach it to one canary, and trace API defaults and per-canary overrides into the runner configuration. The policy enables evaluations; it does not replace the canary's deterministic HTTP contract.

> Runtime verification required: the commands and expected evidence in this chapter are grounded in the current API, controller, and demo fixtures but have not been executed as part of authoring this chapter.

## Prerequisites and starting state

Start at the repository root with `kind-pulse-book`, the manager, model-bearing runner image, `book-shop/catalogue`, and `HttpCanary/catalogue` from earlier chapters. Prepare and build the model artifacts first. Build and load the optional engine explicitly:

```sh
podman build -f Dockerfile.incidentengine -t localhost/pulse-incident-engine:book-v1 .
PULSE_BOOK_ENGINE_ARCHIVE=$(mktemp)
podman save --format docker-archive -o "$PULSE_BOOK_ENGINE_ARCHIVE" localhost/pulse-incident-engine:book-v1
kind load image-archive "$PULSE_BOOK_ENGINE_ARCHIVE" --name pulse-book
```

Docker users replace the three Podman lines with:

```sh
docker build -f Dockerfile.incidentengine -t localhost/pulse-incident-engine:book-v1 .
kind load docker-image localhost/pulse-incident-engine:book-v1 --name pulse-book
```

The installation overlay already sets `PULSE_INCIDENT_ENGINE_IMAGE` to this tag. Confirm the starting canary is deterministic-only:

```sh
kubectl --context kind-pulse-book -n book-shop get httpcanary catalogue \
  -o jsonpath='{.spec.intelligence}{"\n"}'
kubectl --context kind-pulse-book -n pulse-system get deployment pulse-incident-engine
```

The first command should print an empty value. The second should report `NotFound`: a policy object alone does not require an engine, and no canary has opted in yet.

## Write the policy

Create an editable file. An empty trigger block receives CRD defaults; an omitted trigger is disabled. This policy enables all four intelligence paths and a credential-free metric action.

```sh
cat > /tmp/pulse-book-policy.yaml <<'EOF'
apiVersion: canary.iambarton.com/v1alpha1
kind: AnomalyPolicy
metadata:
  name: book-triage
  namespace: book-shop
spec:
  privacy:
    redact:
      - 'Bearer\s+[A-Za-z0-9._-]+'
  triggers:
    bodyDrift: {}
    latencyShift: {}
    failureCorrelation: {}
    failureNovelty: {}
  topology:
    inferDependencies: true
  actions:
    - name: local
      type: metric
EOF
kubectl --context kind-pulse-book apply -f /tmp/pulse-book-policy.yaml
kubectl --context kind-pulse-book -n book-shop get anomalypolicy book-triage -o yaml
```

The API server should default body drift to distance `0.15`, warmup `20`, and two consecutive breaches; latency to z-score `3.0`, warmup `30`, and three breaches; correlation to a 120-second window and similarity `0.85`; novelty to cluster similarity `0.80` and a 300-second settling period. The controller supplies baked-in Potion and ONNX paths when `spec.model` is omitted, plus action throttle defaults of 900 seconds and four firings per hour.

## Attach the policy and add an override

Policy references default to the canary's namespace. The override below changes only this canary's drift threshold, latency threshold, and both warmups; it does not mutate the policy.

```sh
kubectl --context kind-pulse-book -n book-shop patch httpcanary catalogue --type merge -p '
{
  "spec": {
    "intelligence": {
      "enabled": true,
      "policyRef": {"name": "book-triage"},
      "overrides": {
        "driftThreshold": "0.20",
        "latencyZScoreThreshold": "4.0",
        "warmupChecks": 5
      }
    }
  }
}'
kubectl --context kind-pulse-book -n pulse-system rollout status \
  deployment/pulse-incident-engine --timeout=180s
kubectl --context kind-pulse-book -n pulse-system rollout status \
  statefulset/pulse-probe-runner --timeout=180s
```

The opt-in causes the controller to create the single-replica engine and point the runners at it. A cross-namespace policy would require an explicit reference such as `{"name":"shared","namespace":"pulse-system"}`.

## Inspect effective configuration and evidence

```sh
kubectl --context kind-pulse-book -n pulse-system get configmap pulse-probe-config \
  -o jsonpath='{.data.probes\.yaml}' | sed -n '/book-shop\/catalogue/,+55p'
kubectl --context kind-pulse-book -n book-shop get anomalypolicy book-triage \
  -o jsonpath='{.status.referencedBy}{" "}{.status.resolvedHotModel}{" "}{.status.resolvedColdModel}{"\n"}'
kubectl --context kind-pulse-book -n pulse-system logs deployment/pulse-incident-engine \
  --tail=100
```

Expected evidence, requiring later runtime verification:

- the flattened probe names `book-shop/book-triage`;
- the per-canary values `threshold: 0.2`, `zScoreThreshold: 4`, and both warmups at `5`;
- default model paths `/models/potion/model.bin` and `/models/minilm/model.onnx`;
- `referencedBy: 1`, with resolved `potion:` and `onnx:` labels;
- engine logs saying the failure-path model loaded.

The model decides Potion distance, MiniLM similarity, and novelty clustering. EWMA latency, declared-edge matching, root ranking, throttling, HTTP assertions, and engine lifecycle are deterministic.

## Failure symptoms and bounded recovery

A missing policy or unreadable Secret becomes `Invalid intelligence config` in the affected probe; it does not invalidate every canary. A model conflict is reported in policy conditions because model spaces are cluster-wide and the lexicographically first referenced policy wins. Inspect both:

```sh
kubectl --context kind-pulse-book -n book-shop get httpcanary catalogue -o yaml
kubectl --context kind-pulse-book -n book-shop get anomalypolicy book-triage \
  -o jsonpath='{range .status.conditions[*]}{.type}{"="}{.status}{": "}{.message}{"\n"}{end}'
kubectl --context kind-pulse-book -n pulse-system logs statefulset/pulse-probe-runner \
  --all-pods --tail=100
```

Reset only this chapter's opt-in and policy:

```sh
kubectl --context kind-pulse-book -n book-shop patch httpcanary catalogue \
  --type json -p='[{"op":"remove","path":"/spec/intelligence"}]'
kubectl --context kind-pulse-book -n book-shop delete anomalypolicy book-triage \
  --ignore-not-found
kubectl --context kind-pulse-book -n pulse-system wait \
  --for=delete deployment/pulse-incident-engine --timeout=120s
rm -f /tmp/pulse-book-policy.yaml "$PULSE_BOOK_ENGINE_ARCHIVE"
```

Reapply the policy and patch before continuing to the Potion chapter.

## Checkpoint

Why does creating a policy not start the engine? Which settings are defaults, which are policy values, and which are copied into a per-canary configuration? Explain why two referenced policies cannot safely select unrelated vector spaces.
