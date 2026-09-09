# Validate an MCP tool catalogue

## Objective

Perform the exact three-message MCP exchange used by Pulse—`initialize`, `notifications/initialized`, and `tools/list`—then configure an MCP canary and diagnose a missing required tool.

## Prerequisites and starting state

The manager and runner must be Ready in `kind-pulse-book`; `book-shop` must exist; and `localhost/pulse-demo-target:book-v1` must already be loaded as described by the installation course. This chapter uses the fixture's Streamable-HTTP-shaped JSON-RPC endpoint, not an external MCP server.

```sh
kubectl --context kind-pulse-book -n pulse-system rollout status statefulset/pulse-probe-runner --timeout=180s
kubectl --context kind-pulse-book get namespace book-shop
```

Deploy the editable target YAML and wait:

```sh
kubectl --context kind-pulse-book apply -f book/examples/protocols/mcp-target.yaml
kubectl --context kind-pulse-book -n book-shop rollout status deployment/mcp --timeout=120s
kubectl --context kind-pulse-book -n book-shop get endpointslice -l kubernetes.io/service-name=mcp
```

Pod names, addresses, and resource versions are variable. A missing endpoint usually means the Pod is not Ready or the Service selector does not match `app: book-mcp`.

## Exchange the protocol manually

Start a dedicated inspection tunnel:

```sh
kubectl --context kind-pulse-book -n book-shop port-forward service/mcp 18081:8080
```

In another terminal, initialize:

```sh
curl --fail --max-time 5 -sS -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":"manual-initialize","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"pulse-book","version":"1.0.0"}}}' \
  http://127.0.0.1:18081/mcp
```

Expect a JSON-RPC result with protocol version `2025-11-25`, server name `pulse-demo-mcp`, and a nonempty `capabilities.tools` object. Object key order and whitespace are variable.

Send the required initialized notification:

```sh
curl --fail --max-time 5 -sS -o /dev/null -w '%{http_code}\n' \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  http://127.0.0.1:18081/mcp
```

The fixture returns HTTP `202` with no JSON-RPC body. Pulse accepts any 2xx response for this notification.

Finally list tools:

```sh
curl --fail --max-time 5 -sS -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":"manual-tools-list","method":"tools/list"}' \
  http://127.0.0.1:18081/mcp
```

Expect `orders.lookup` and `health.check`. This manual sequence independently proves what the fixture currently advertises.

## Configure Pulse

Open `book/examples/protocols/mcp-canary.yaml`. The editable MCP portion is:

```yaml
mcp:
  protocolVersion: "2025-11-25"
  clientName: pulse
  clientVersion: "0.1.0"
  requireToolsCapability: true
  minToolCount: 1
  requiredTools:
    - health.check
```

Pulse posts JSON-RPC to `spec.url`, adds JSON content and accept headers unless explicitly supplied, checks that initialize returns HTTP 200 and a nonempty protocol version, optionally requires a tools capability, accepts a 2xx initialized notification, then requires tools/list HTTP 200 and validates count and exact tool names. The implementation currently checks only that the returned protocol version is nonempty; it does **not** enforce equality with the requested value.

Apply and inspect:

```sh
kubectl --context kind-pulse-book apply -f book/examples/protocols/mcp-canary.yaml
kubectl --context kind-pulse-book -n book-shop wait httpcanary/mcp-tools --for=jsonpath='{.status.phase}'=Healthy --timeout=120s
kubectl --context kind-pulse-book -n book-shop get httpcanary mcp-tools -o jsonpath='{.status.lastStatus}{"\t"}{.status.message}{"\n"}'
kubectl --context kind-pulse-book -n pulse-system get configmap pulse-probe-config -o yaml
```

Expect status `200` and `MCP tool validation succeeded (2 tools)`. Tool order in the independent response and ConfigMap metadata are variable.

`expectedStatus` remains part of the `HttpCanary` schema, but MCP initialize and tools/list specifically require HTTP 200 in the runner; changing top-level `expectedStatus` does not change those MCP stage checks. Secret-backed `spec.auth` and `spec.headers` apply to all three requests.

## Remove a required tool

Use the fixture's control endpoint and inspect the mutation before waiting on Pulse:

```sh
curl --fail --max-time 5 -sS -H 'Content-Type: application/json' \
  -d '{"behavior":"mcp-missing-tool"}' http://127.0.0.1:18081/__control
curl --fail --max-time 5 -sS http://127.0.0.1:18081/__control
curl --fail --max-time 5 -sS -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":"manual-tools-list","method":"tools/list"}' \
  http://127.0.0.1:18081/mcp
kubectl --context kind-pulse-book -n book-shop wait httpcanary/mcp-tools --for=jsonpath='{.status.phase}'=Unhealthy --timeout=120s
kubectl --context kind-pulse-book -n book-shop get httpcanary mcp-tools -o jsonpath='{.status.lastStatus}{"\t"}{.status.message}{"\n"}'
```

The manual list should now contain only `orders.lookup`. Pulse should report `tools/list missing required tools: health.check`. This is deterministic schema/content validation, not model inference.

If the wait expires, distinguish configuration, protocol, and connectivity failures:

```sh
kubectl --context kind-pulse-book -n book-shop get httpcanary mcp-tools -o yaml
kubectl --context kind-pulse-book -n pulse-system logs statefulset/pulse-probe-runner --since=5m
kubectl --context kind-pulse-book -n book-shop get endpointslice -l kubernetes.io/service-name=mcp
```

Messages name the failed stage, such as `initialize returned HTTP ...`, `notifications/initialized returned HTTP ...`, or `tools/list ...`. Status `0` indicates no accepted HTTP response.

## Current protocol boundary

This canary supports only the exchange documented above and ordinary JSON response bodies. It does not consume server-sent event streams, retain an `Mcp-Session-Id`, paginate tools/list, call tools, subscribe to list changes, negotiate authentication, or validate tool input schemas. The demo endpoint is intentionally stateless and returns JSON even though its Accept header also names `text/event-stream`; passing this fixture is not proof of compatibility with every MCP transport.

All responses share the runner's fixed 10-second HTTP timeout and 4 MiB body ceiling. Neither is configurable in `HttpCanary`.

## Restore

```sh
curl --fail --max-time 5 -sS -H 'Content-Type: application/json' \
  -d '{"behavior":"healthy"}' http://127.0.0.1:18081/__control
kubectl --context kind-pulse-book -n book-shop wait httpcanary/mcp-tools --for=jsonpath='{.status.phase}'=Healthy --timeout=120s
kubectl --context kind-pulse-book -n book-shop get httpcanary mcp-tools -o jsonpath='{.status.message}{"\n"}'
kubectl --context kind-pulse-book delete -f book/examples/protocols/mcp-canary.yaml --ignore-not-found
kubectl --context kind-pulse-book delete -f book/examples/protocols/mcp-target.yaml --ignore-not-found
```

Stop the port-forward with Ctrl-C.

## Checkpoint exercise

Edit `requiredTools` to contain both fixture tools, apply, and predict the healthy count. Then add `inventory.reserve`, inspect tools/list manually, and explain why `minToolCount: 1` cannot make the canary pass. Restore the checked-in YAML and fixture behavior.
