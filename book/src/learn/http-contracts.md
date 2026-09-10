# Extend an HTTP contract

## Objective

Configure and diagnose Pulse's deterministic HTTP status, body, request-header, and Secret-backed authentication checks. You will also identify the timeout, TLS, redirect, and response-size behavior that is fixed in the current runner rather than configurable in an `HttpCanary`.

## Prerequisites and starting state

Complete the installation and first-canary chapters. The `kind-pulse-book` context must contain a Ready `pulse-probe-runner`, the `book-shop` namespace, and the `catalogue` Deployment and Service from `book/examples/first-canary/target.yaml`. Keep the repository root as your working directory.

Confirm that state with bounded checks:

```sh
kubectl --context kind-pulse-book -n pulse-system rollout status statefulset/pulse-probe-runner --timeout=180s
kubectl --context kind-pulse-book -n book-shop rollout status deployment/catalogue --timeout=120s
kubectl --context kind-pulse-book -n book-shop get endpointslice -l kubernetes.io/service-name=catalogue
```

The endpoint addresses and Pod names are variable. At least one ready endpoint is required.

## Inspect the target first

Run this in a dedicated terminal:

```sh
kubectl --context kind-pulse-book -n book-shop port-forward service/catalogue 18080:8080
```

From another terminal, bound the request and inspect status, headers, and body:

```sh
curl --fail --max-time 5 -i http://127.0.0.1:18080/
```

Expect HTTP `200`, a `Content-Type: application/json` response header, and a body containing `items`. Header order, `Date`, `Content-Length`, and the port-forward Pod name are variable.

## Write status, body, header, and auth configuration

Open `book/examples/protocols/http-contract.yaml` and edit it as ordinary YAML. It creates a namespaced Secret and this contract:

```yaml
spec:
  url: http://catalogue.book-shop.svc:8080/
  method: GET
  headers:
    X-Pulse-Course: http-contracts
  auth:
    type: bearer
    bearer:
      tokenSecretRef:
        name: catalogue-auth
        key: token
  interval: 10
  expectedStatus: 200
  containsText: items
```

Apply and wait for the exact contract result:

```sh
kubectl --context kind-pulse-book apply -f book/examples/protocols/http-contract.yaml
kubectl --context kind-pulse-book -n book-shop wait httpcanary/catalogue-contract --for=jsonpath='{.status.phase}'=Healthy --timeout=120s
kubectl --context kind-pulse-book -n book-shop get httpcanary catalogue-contract -o jsonpath='{.status.lastStatus}{"\t"}{.status.message}{"\n"}'
kubectl --context kind-pulse-book -n pulse-system get configmap pulse-probe-config -o yaml
```

Expect status `200` and `Got expected status 200 and matched response text`. Locate `X-Pulse-Course` and `book-shop/catalogue-contract` in the rendered configuration. The demo catalogue deliberately ignores unknown headers and authorization, so this passing result proves status/body evaluation and successful Secret resolution, but it does **not** prove what headers arrived at the target. Do not claim receiver-side auth validation from this fixture.

Pulse supports three Secret-backed auth shapes: `basic` with `usernameSecretRef` and `passwordSecretRef`, `bearer` with `tokenSecretRef`, and `apiKey` with `headerName` and `valueSecretRef`. Secret references are resolved in the canary's namespace. Credential values are stored in the operator-managed `pulse-probe-auth` Secret, not in the ConfigMap or canary status. A missing Secret or key produces an unhealthy result beginning `Invalid auth config:`. Do not print the managed Secret's contents in course transcripts.

`spec.headers` sets request headers. The current API has no response-header assertion field. A header map therefore cannot assert the target's `Content-Type`.

## Introduce and inspect an assertion failure

Change only the expected status:

```sh
kubectl --context kind-pulse-book -n book-shop patch httpcanary catalogue-contract --type merge -p '{"spec":{"expectedStatus":201}}'
kubectl --context kind-pulse-book -n book-shop wait httpcanary/catalogue-contract --for=jsonpath='{.status.phase}'=Unhealthy --timeout=120s
kubectl --context kind-pulse-book -n book-shop get httpcanary catalogue-contract -o jsonpath='{.status.lastStatus}{"\t"}{.status.message}{"\n"}'
curl --fail --max-time 5 -i http://127.0.0.1:18080/
```

The independent request remains HTTP `200`; Pulse should report `Expected 201 but got 200`. If the wait expires, inspect current live results and runner logs:

```sh
kubectl --context kind-pulse-book -n pulse-system port-forward service/pulse-probe-runner 19091:9091
```

In another terminal:

```sh
PULSE_INTERNAL_TOKEN=$(kubectl --context kind-pulse-book -n pulse-system get \
  secret/pulse-probe-auth -o jsonpath='{.data.internal-token}' | base64 --decode)
curl --fail --max-time 5 -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" \
  http://127.0.0.1:19091/results
unset PULSE_INTERNAL_TOKEN
kubectl --context kind-pulse-book -n pulse-system logs statefulset/pulse-probe-runner --since=5m
```

Result ordering, timestamps, and durations are variable. Stop this diagnostic port-forward with Ctrl-C.

## Fixed behavior and unsupported knobs

These are implementation properties of the current runner, not CRD fields:

- Every HTTP request has a fixed 10-second client timeout. There is no per-canary timeout field. The fixture's `slow` behavior sleeps for only two seconds, so it does not demonstrate timeout failure.
- HTTPS uses Go's default transport and host trust roots, including ordinary certificate and hostname verification. There is no CA-bundle, client-certificate, server-name, or insecure-skip-verify field. The course fixture exposes HTTP only, so this chapter makes no TLS success claim.
- The client follows Go's default redirect policy. There is no redirect policy field.
- Every HTTP-like response, including journey and MCP responses, has a fixed 4 MiB (4,194,304-byte) safety ceiling. A larger body fails with `response body exceeded the 4194304-byte safety limit`; the CRD cannot change that ceiling. `AnomalyPolicy`'s separate `maxBodyBytes` limits model input and does not alter deterministic assertion reading.
- `containsText` is a literal, case-sensitive substring check. Regex, JSONPath, schema, and response-header assertions are unsupported.

Typical transport failures have status `0` and a message beginning `http request failed:`. An assertion mismatch preserves the received HTTP status. That distinction tells you whether a response existed.

## Restore

Reapply the editable declaration and require recovery:

```sh
kubectl --context kind-pulse-book apply -f book/examples/protocols/http-contract.yaml
kubectl --context kind-pulse-book -n book-shop wait httpcanary/catalogue-contract --for=jsonpath='{.status.phase}'=Healthy --timeout=120s
kubectl --context kind-pulse-book -n book-shop get httpcanary catalogue-contract -o jsonpath='{.status.lastStatus}{"\t"}{.status.message}{"\n"}'
```

Stop the catalogue port-forward with Ctrl-C. To remove only this chapter's objects after the checkpoint:

```sh
kubectl --context kind-pulse-book delete -f book/examples/protocols/http-contract.yaml --ignore-not-found
```

## Checkpoint exercise

Change `containsText` to `total`, wait for a healthy result, then change it to `Items` and explain the failure using the independent body. Restore by reapplying the example. Which evidence proves the target responded, and which evidence proves the contract matched?
