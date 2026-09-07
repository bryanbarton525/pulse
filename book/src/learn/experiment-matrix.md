# Intelligence experiment matrix

## Learning objective

Run each supported failure shape manually, capture independent application evidence and Pulse evidence, and restore it before the next experiment. The matrix distinguishes deterministic validation from model contribution.

> Runtime verification required: this is a run sheet grounded in `hack/demo/00-targets.yaml`, `10-policy.yaml`, `20-canaries.yaml`, and current runtime code. No row was executed while authoring this chapter.

## Prerequisites and starting state

Use `kind-pulse-book` with the `shop` targets, `demo-triage`, canaries, real Potion runner, real ONNX MiniLM engine, and local sink from the preceding chapters. The demo control endpoint mutates one process in place; it simulates changed release behavior but is not a Kubernetes blue/green rollout.

Before every row, require healthy fresh results and no incidents:

```sh
for canary in catalogue checkout search no-content login-journey mcp-tools similar-a similar-b unrelated; do
  kubectl --context kind-pulse-book -n shop wait httpcanary/"$canary" \
    --for=jsonpath='{.status.phase}'=Healthy --timeout=180s
done
kubectl --context kind-pulse-book -n shop wait grpccanary/orders \
  --for=jsonpath='{.status.phase}'=Healthy --timeout=180s
kubectl --context kind-pulse-book -n pulse-system get --raw \
  '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/results' |
  python3 -m json.tool
kubectl --context kind-pulse-book -n pulse-system get --raw \
  '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/incidents'
```

For drift, require `driftState=ready` and its configured minimum sample count. For latency, require `latencyState=ready`. For novelty, wait beyond `settlingPeriodSeconds`. For failure correlation, mutations must occur within `windowSeconds`.

After each mutation, use these inspections:

```sh
kubectl --context kind-pulse-book -n pulse-system get --raw \
  '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/results' |
  python3 -m json.tool
kubectl --context kind-pulse-book -n pulse-system get --raw \
  '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/incidents' |
  python3 -m json.tool
kubectl --context kind-pulse-book -n pulse-system get --raw \
  '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/metrics' |
  grep -E '^pulse_(incidents|incident_members|incident_actions|incident_actions_throttled)'
kubectl --context kind-pulse-book -n pulse-demo logs deployment/sink --since=10m
```

Variable evidence is the timestamp, incident ID, score, live age, and cumulative metric count. Record it rather than expecting a fixed literal.

## Matrix

| Experiment | Baseline and mutation | Expected Pulse evidence | Model versus deterministic logic | Negative control and restore |
| --- | --- | --- | --- | --- |
| Status mismatch | `no-content` is fresh and Healthy. Set catalogue to `no-content-fail`; independently GET `/health/no-content`. | HTTP 200 conflicts with declared 204; `no-content` becomes Unhealthy and opens one incident. | No model detects the mismatch. MiniLM may classify its already-detected failure for novelty. | Other catalogue routes remain valid. Restore catalogue `healthy`. |
| Missing body marker | `unrelated` requires `healthy-control`. Set it to `control-fail`; independently GET `/`. | HTTP remains 200; message names missing marker; one incident. | `containsText` is deterministic. Cold embedding is only correlation/novelty. | `similar-*` remains Healthy because `/similar` is unchanged. Restore `healthy`. |
| Journey/session | Both steps pass and cookie jar carries `pulse-demo-session`. Set catalogue to `journey-fail`; manually request login and session with a cookie jar. | Message identifies step 2 `read-session`; status 200 but marker `authenticated` is absent. | Journey order, cookies, and assertions are deterministic. MiniLM does not execute the journey. | Plain catalogue remains Healthy. Restore `healthy`. |
| Missing MCP tool | MCP initializes and lists `health.check`. Set MCP to `mcp-missing-tool`; perform initialize and tools/list manually. | Reachable server, tools capability present, but message names missing `health.check`. | Protocol exchange and set membership are deterministic. Cold model only handles the resulting failure text. | `orders.lookup` remains listed. Restore `healthy`. |
| gRPC not serving | `shop.Orders` reports SERVING. Set control port to `grpc-fail`; query health with a Go client. | Transport succeeds; status message is `NOT_SERVING`, distinct from code 14 UNAVAILABLE. | gRPC health result is deterministic. MiniLM may be unavailable for text-poor failures without affecting it. | TCP endpoint remains reachable. Restore `healthy`. |
| Passing semantic drift | Catalogue Potion baseline is ready. Set catalogue to `green`; independently GET normal 200 body with `items: []`. | Phase stays Healthy; two breaches above drift threshold open body-drift incident with measured distance and samples. | Potion supplies 512-vector and distance. Pass, baseline update, threshold, debounce, incident are deterministic. | `containsText: items` still passes. Restore `healthy`. |
| Latency shift | Catalogue latency baseline is ready. Set catalogue to `slow`; time direct GET. | Phase stays Healthy; three z-scores above threshold open a latency incident. Real checkout/search callers may get separate shifts. | No model. Duration, EWMA mean/variance, floor, threshold, debounce are deterministic. | Body remains normal and passing. Restore `healthy`. |
| Declared dependency outage | Catalogue/checkout/search Healthy; unrelated Healthy. Set catalogue `outage` and unrelated `control-fail` within 120 seconds. | Three related failures share an ID with catalogue root and declared-edge evidence; unrelated has a separate ID. | Declared edge needs no model. Root and roles are deterministic. | Unrelated is the timing-only negative control. Restore both `healthy`. |
| Similarity-only grouping | `similar-a/b` have no edge; orders Healthy. Set unrelated `similarity-fail` and orders `grpc-fail`. | Pair shares one ID with numeric similarity at least 0.85; orders remains separate. | MiniLM supplies 384-vectors and cosine similarity. Window, threshold, merge and IDs are deterministic. | Dissimilar gRPC failure is the control. Restore both services. |
| Novelty replay | Engine is past settling with empty cluster index; catalogue topology group Healthy. Run outage, recover fully, then run identical outage. | First incident `novel:true`; replay has a new ID and `novel:false`; familiar shape skips LLM only. | MiniLM supplies cluster similarity. Cluster threshold, occurrences, action routing and throttle are deterministic. | Change failure text for a counterexample likely to form a new cluster, but measure it. Restore `healthy`. |
| Missing models | All Healthy. Override hot/cold paths to nonexistent files and wait for explicit load errors. Run topology outage. | Assertions and declared-edge incident continue; no drift, similarity, or novelty evidence. | Models contribute nothing while absent. Deterministic monitoring/topology remain. | Similarity-only pair should not merge. Remove `spec.model` and restart workloads. |
| Stale results | Record live times and ages. Delete a runner Pod or hold a shard unavailable. | Last aggregate ages; after two minutes that shard is omitted. CR status remains previous rather than being fabricated. | No model. Timeout, omission, fallback polling, and preserved CR are deterministic. | Other live shard continues advancing. Restore replica and require fresh time. |

## Exact mutation and independent-check commands

Every mutation uses the fixture's explicit control request:

```sh
# Status mismatch.
printf '%s' '{"behavior":"no-content-fail"}' |
  kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:catalogue:8080/proxy/__control' -f -
kubectl --context kind-pulse-book get --raw \
  '/api/v1/namespaces/shop/services/http:catalogue:8080/proxy/health/no-content'

# Body marker and similarity pair.
printf '%s' '{"behavior":"control-fail"}' |
  kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:unrelated:8080/proxy/__control' -f -
kubectl --context kind-pulse-book get --raw \
  '/api/v1/namespaces/shop/services/http:unrelated:8080/proxy/'
printf '%s' '{"behavior":"similarity-fail"}' |
  kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:unrelated:8080/proxy/__control' -f -
kubectl --context kind-pulse-book get --raw \
  '/api/v1/namespaces/shop/services/http:unrelated:8080/proxy/similar'

# Journey; curl carries the real cookie between steps.
printf '%s' '{"behavior":"journey-fail"}' |
  kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:catalogue:8080/proxy/__control' -f -
kubectl --context kind-pulse-book -n shop port-forward service/catalogue 18080:8080
```

In a second terminal:

```sh
COOKIE_JAR=$(mktemp)
curl --fail --max-time 5 -c "$COOKIE_JAR" http://127.0.0.1:18080/login
curl --fail --max-time 5 -b "$COOKIE_JAR" http://127.0.0.1:18080/session
rm -f "$COOKIE_JAR"
```

Stop the port-forward with Ctrl-C.

```sh
# MCP mutation and manual JSON-RPC exchange.
printf '%s' '{"behavior":"mcp-missing-tool"}' |
  kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:mcp:8080/proxy/__control' -f -
kubectl --context kind-pulse-book -n shop port-forward service/mcp 18081:8080
```

In a second terminal:

```sh
curl --fail --max-time 5 -sS http://127.0.0.1:18081/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":"manual-init","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"book","version":"1"}}}'
curl --fail --max-time 5 -sS http://127.0.0.1:18081/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'
curl --fail --max-time 5 -sS http://127.0.0.1:18081/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":"manual-tools","method":"tools/list"}'
```

Stop the port-forward with Ctrl-C.

```sh
# gRPC mutation and an exact temporary Go health client.
printf '%s' '{"behavior":"grpc-fail"}' |
  kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:orders-grpc:8080/proxy/__control' -f -
kubectl --context kind-pulse-book -n shop port-forward service/orders-grpc 15051:50051
```

In a second terminal:

```sh
cat > /tmp/pulse-book-grpc-health.go <<'EOF'
package main
import (
  "context"
  "fmt"
  "time"
  "google.golang.org/grpc"
  "google.golang.org/grpc/credentials/insecure"
  healthpb "google.golang.org/grpc/health/grpc_health_v1"
)
func main() {
  ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
  defer cancel()
  conn, err := grpc.NewClient("127.0.0.1:15051",
    grpc.WithTransportCredentials(insecure.NewCredentials()))
  if err != nil { panic(err) }
  defer conn.Close()
  reply, err := healthpb.NewHealthClient(conn).Check(ctx,
    &healthpb.HealthCheckRequest{Service: "shop.Orders"})
  if err != nil { panic(err) }
  fmt.Println(reply.Status)
}
EOF
go run /tmp/pulse-book-grpc-health.go
rm -f /tmp/pulse-book-grpc-health.go
```

Stop the port-forward with Ctrl-C.

```sh
# Drift, latency, dependency outage, and their direct responses.
for behavior in green slow outage; do
  printf '{"behavior":"%s"}' "$behavior" |
    kubectl --context kind-pulse-book create --raw \
    '/api/v1/namespaces/shop/services/http:catalogue:8080/proxy/__control' -f -
  time kubectl --context kind-pulse-book get --raw \
    '/api/v1/namespaces/shop/services/http:catalogue:8080/proxy/' || true
done
```

Run those three behaviors as separate experiments with a full restore between them, not as one unattended sequence.

## Failure symptoms and bounded recovery

For a named HTTP result:

```sh
CANARY=unrelated
WANT=Unhealthy
kubectl --context kind-pulse-book -n shop wait httpcanary/"$CANARY" \
  --for=jsonpath='{.status.phase}'="$WANT" --timeout=180s
kubectl --context kind-pulse-book -n shop get httpcanary "$CANARY" -o yaml
```

Restore all fixture controls after any failed or interrupted row:

```sh
for service in catalogue checkout search unrelated mcp; do
  printf '%s' '{"behavior":"healthy"}' |
    kubectl --context kind-pulse-book create --raw \
    "/api/v1/namespaces/shop/services/http:${service}:8080/proxy/__control" -f -
done
printf '%s' '{"behavior":"healthy"}' |
  kubectl --context kind-pulse-book create --raw \
  '/api/v1/namespaces/shop/services/http:orders-grpc:8080/proxy/__control' -f -
for canary in catalogue checkout search no-content login-journey mcp-tools similar-a similar-b unrelated; do
  kubectl --context kind-pulse-book -n shop wait httpcanary/"$canary" \
    --for=jsonpath='{.status.phase}'=Healthy --timeout=180s
done
kubectl --context kind-pulse-book -n shop wait grpccanary/orders \
  --for=jsonpath='{.status.phase}'=Healthy --timeout=180s
```

If a wait fails, inspect the direct protocol response, live result time, runner logs, engine model-load logs, incident evidence, and sink counts. Increasing the timeout without identifying which layer is stale is not recovery.

## Checkpoint

Choose one deterministic failure, one Potion signal, one MiniLM merge, and one state-lifetime experiment. For each, state the minimum readiness evidence, independent control observation, expected incident/action evidence, negative control, and exact restore.
