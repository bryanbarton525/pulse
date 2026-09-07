# Probe named gRPC health

## Objective

Query the standard `grpc.health.v1.Health` service for the named service `shop.Orders`, configure a `GrpcCanary`, and distinguish an application-level `NOT_SERVING` response from a transport failure.

## Prerequisites and starting state

The `kind-pulse-book` cluster must have a Ready manager and runner, the `book-shop` namespace, and the already-loaded `localhost/pulse-demo-target:book-v1` image. Install `grpc-health-probe` on the host and confirm it is callable:

```sh
grpc-health-probe -version
kubectl --context kind-pulse-book -n pulse-system rollout status statefulset/pulse-probe-runner --timeout=180s
kubectl --context kind-pulse-book get namespace book-shop
```

`grpc-health-probe` is used instead of `grpcurl` because this fixture registers the standard health service but does not register gRPC server reflection. `grpcurl` would therefore also need a local health `.proto` or descriptor set. The health probe is an accurate client for the same unary `grpc.health.v1.Health/Check` RPC Pulse calls.

Deploy the editable target:

```sh
kubectl --context kind-pulse-book apply -f book/examples/protocols/grpc-target.yaml
kubectl --context kind-pulse-book -n book-shop rollout status deployment/orders-grpc --timeout=120s
kubectl --context kind-pulse-book -n book-shop get endpointslice -l kubernetes.io/service-name=orders-grpc
```

Expect one ready endpoint with gRPC port 50051 and control port 8080. Pod names and endpoint addresses are variable.

## Query the named service independently

In a dedicated terminal, forward both Service ports:

```sh
kubectl --context kind-pulse-book -n book-shop port-forward service/orders-grpc 15051:50051 18082:8080
```

From another terminal:

```sh
grpc-health-probe -addr=127.0.0.1:15051 -service=shop.Orders -connect-timeout=3s -rpc-timeout=3s
curl --fail --max-time 5 http://127.0.0.1:18082/__control
```

Expect `status: SERVING` and exit code zero. The control response should report `healthy`; JSON whitespace is variable.

## Configure Pulse

Open `book/examples/protocols/grpc-canary.yaml`. Its current CRD fields are:

```yaml
spec:
  url: orders-grpc.book-shop.svc:50051
  service: shop.Orders
  interval: 10
  outputs:
    - type: prometheus
```

`service` becomes the `HealthCheckRequest.service` string. An empty value checks the server's overall health instead. Pulse uses plaintext/insecure gRPC transport and a fixed five-second context deadline, and passes only when the RPC succeeds with `SERVING`.

Apply and inspect:

```sh
kubectl --context kind-pulse-book apply -f book/examples/protocols/grpc-canary.yaml
kubectl --context kind-pulse-book -n book-shop wait grpccanary/orders --for=jsonpath='{.status.phase}'=Healthy --timeout=120s
kubectl --context kind-pulse-book -n book-shop get grpccanary orders -o jsonpath='{.status.lastStatus}{"\t"}{.status.message}{"\n"}'
kubectl --context kind-pulse-book -n pulse-system get configmap pulse-probe-config -o yaml
```

Expect gRPC status code `0` (`OK`) and `gRPC health check succeeded`. Find `grpcService: shop.Orders` in the rendered probe. Timestamps and configuration metadata are variable.

## Application health failure: NOT_SERVING

Mutate the fixture and inspect the RPC directly:

```sh
curl --fail --max-time 5 -H 'Content-Type: application/json' \
  -d '{"behavior":"grpc-fail"}' http://127.0.0.1:18082/__control
curl --fail --max-time 5 http://127.0.0.1:18082/__control
grpc-health-probe -addr=127.0.0.1:15051 -service=shop.Orders -connect-timeout=3s -rpc-timeout=3s
kubectl --context kind-pulse-book -n book-shop wait grpccanary/orders --for=jsonpath='{.status.phase}'=Unhealthy --timeout=120s
kubectl --context kind-pulse-book -n book-shop get grpccanary orders -o jsonpath='{.status.lastStatus}{"\t"}{.status.message}{"\n"}'
```

The health client exits nonzero and reports `status: NOT_SERVING`, but it did reach the server and receive a valid health response. Pulse reports status `0` and `Service is not SERVING, status: NOT_SERVING`. Here status `0` describes the successful gRPC RPC; the health payload makes the canary unhealthy.

Restore application health before testing transport:

```sh
curl --fail --max-time 5 -H 'Content-Type: application/json' \
  -d '{"behavior":"healthy"}' http://127.0.0.1:18082/__control
kubectl --context kind-pulse-book -n book-shop wait grpccanary/orders --for=jsonpath='{.status.phase}'=Healthy --timeout=120s
```

## Transport failure

Stop the port-forward with Ctrl-C, scale away the only server, and bound the wait for Pod deletion:

```sh
kubectl --context kind-pulse-book -n book-shop scale deployment/orders-grpc --replicas=0
kubectl --context kind-pulse-book -n book-shop wait --for=delete pod -l app=book-orders-grpc --timeout=120s
kubectl --context kind-pulse-book -n book-shop get endpointslice -l kubernetes.io/service-name=orders-grpc
kubectl --context kind-pulse-book -n book-shop wait grpccanary/orders --for=jsonpath='{.status.phase}'=Unhealthy --timeout=120s
kubectl --context kind-pulse-book -n book-shop get grpccanary orders -o jsonpath='{.status.lastStatus}{"\t"}{.status.message}{"\n"}'
```

The EndpointSlice should have no ready target. Pulse uses status `14` (`UNAVAILABLE`) when the health RPC returns an error and a message beginning `Health check failed:`. The exact resolver or connection error text is variable. Unlike `NOT_SERVING`, there is no valid health response.

For an independent transport check, this bounded local attempt intentionally has no working port-forward:

```sh
grpc-health-probe -addr=127.0.0.1:15051 -service=shop.Orders -connect-timeout=3s -rpc-timeout=3s
```

Expect a nonzero exit and a connection error; the OS-specific text varies.

If a Pulse wait expires, inspect endpoints, source status, and logs:

```sh
kubectl --context kind-pulse-book -n book-shop get endpointslice -l kubernetes.io/service-name=orders-grpc -o yaml
kubectl --context kind-pulse-book -n book-shop get grpccanary orders -o yaml
kubectl --context kind-pulse-book -n pulse-system logs statefulset/pulse-probe-runner --since=5m
```

## Current protocol boundary

`GrpcCanary` supports only standard unary health checks over plaintext transport. The current CRD has no TLS, mTLS, authority, metadata/authentication, compression, custom RPC, request payload, expected non-health status, reflection, or per-canary deadline field. Do not add those names to YAML: the CRD does not define them.

## Restore

Reapply the target to restore one replica, then require both direct and Pulse health:

```sh
kubectl --context kind-pulse-book apply -f book/examples/protocols/grpc-target.yaml
kubectl --context kind-pulse-book -n book-shop rollout status deployment/orders-grpc --timeout=120s
kubectl --context kind-pulse-book -n book-shop wait grpccanary/orders --for=jsonpath='{.status.phase}'=Healthy --timeout=120s
```

Restart the port-forward in a dedicated terminal:

```sh
kubectl --context kind-pulse-book -n book-shop port-forward service/orders-grpc 15051:50051 18082:8080
```

Verify:

```sh
grpc-health-probe -addr=127.0.0.1:15051 -service=shop.Orders -connect-timeout=3s -rpc-timeout=3s
kubectl --context kind-pulse-book -n book-shop get grpccanary orders -o jsonpath='{.status.lastStatus}{"\t"}{.status.message}{"\n"}'
kubectl --context kind-pulse-book delete -f book/examples/protocols/grpc-canary.yaml --ignore-not-found
kubectl --context kind-pulse-book delete -f book/examples/protocols/grpc-target.yaml --ignore-not-found
```

Stop the port-forward with Ctrl-C.

## Checkpoint exercise

Change `service` to `shop.Missing`, apply, and inspect the direct and Pulse results. Is an unknown named service a transport failure or a health response whose status is not `SERVING`? Restore the checked-in YAML and explain which status/message evidence separates the two failure classes.
