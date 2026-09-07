# Check 204 responses and cookie journeys

## Objective

Verify a bodyless HTTP `204` contract and a two-step login journey whose second request depends on the cookie set by the first. Then create deterministic failures and identify the failing step from Pulse status and direct target evidence.

## Prerequisites and starting state

The `kind-pulse-book` cluster must have the installed manager and runner plus the `book-shop/catalogue` target from the first-canary chapter. The target must be in its `healthy` behavior.

```sh
kubectl --context kind-pulse-book -n pulse-system rollout status statefulset/pulse-probe-runner --timeout=180s
kubectl --context kind-pulse-book -n book-shop rollout status deployment/catalogue --timeout=120s
kubectl --context kind-pulse-book -n book-shop get endpointslice -l kubernetes.io/service-name=catalogue
```

Start a target inspection tunnel in a dedicated terminal:

```sh
kubectl --context kind-pulse-book -n book-shop port-forward service/catalogue 18080:8080
```

## A bodyless 204 contract

Inspect `book/examples/protocols/no-content.yaml`. Its editable assertion is intentionally only a status:

```yaml
spec:
  url: http://catalogue.book-shop.svc:8080/health/no-content
  interval: 10
  expectedStatus: 204
```

First inspect the endpoint independently, then apply the contract:

```sh
curl --max-time 5 -i http://127.0.0.1:18080/health/no-content
kubectl --context kind-pulse-book apply -f book/examples/protocols/no-content.yaml
kubectl --context kind-pulse-book -n book-shop wait httpcanary/catalogue-no-content --for=jsonpath='{.status.phase}'=Healthy --timeout=120s
kubectl --context kind-pulse-book -n book-shop get httpcanary catalogue-no-content -o jsonpath='{.status.lastStatus}{"\t"}{.status.message}{"\n"}'
```

Expect `HTTP/1.1 204 No Content`, an empty body, status `204`, and `Got expected status 204`. Generated headers such as `Date` are variable. A `204` needs no body marker; adding one would require content that a conforming 204 response does not carry.

Cause the fixture's documented 204 failure:

```sh
curl --fail --max-time 5 -H 'Content-Type: application/json' -d '{"behavior":"no-content-fail"}' http://127.0.0.1:18080/__control
curl --fail --max-time 5 http://127.0.0.1:18080/__control
curl --max-time 5 -i http://127.0.0.1:18080/health/no-content
kubectl --context kind-pulse-book -n book-shop wait httpcanary/catalogue-no-content --for=jsonpath='{.status.phase}'=Unhealthy --timeout=120s
kubectl --context kind-pulse-book -n book-shop get httpcanary catalogue-no-content -o jsonpath='{.status.lastStatus}{"\t"}{.status.message}{"\n"}'
```

The control response should contain `"no-content-fail"`; JSON spacing may vary. The endpoint now returns `200` and `unexpected body`, while Pulse reports `Expected 204 but got 200`. This control endpoint mutates the in-process course fixture; it is not a Kubernetes rollout.

Restore before the journey:

```sh
curl --fail --max-time 5 -H 'Content-Type: application/json' -d '{"behavior":"healthy"}' http://127.0.0.1:18080/__control
kubectl --context kind-pulse-book -n book-shop wait httpcanary/catalogue-no-content --for=jsonpath='{.status.phase}'=Healthy --timeout=120s
```

## A two-step session

Open `book/examples/protocols/login-journey.yaml`. Each step has its own URL and assertions:

```yaml
journey:
  - name: open-login
    url: http://catalogue.book-shop.svc:8080/login
    method: GET
    expectedStatus: 200
    containsText: Sign in
  - name: read-session
    url: http://catalogue.book-shop.svc:8080/session
    method: GET
    expectedStatus: 200
    containsText: authenticated
```

The runner creates one cookie jar for a check and reuses the same HTTP client across its steps. `/login` sets the `pulse-demo-session=authenticated` cookie; `/session` requires it. The top-level `url` identifies the probe in its final result, while `journey` supplies the requests actually executed.

Show that the second endpoint fails without that cookie:

```sh
curl --max-time 5 -i http://127.0.0.1:18080/session
curl --fail --max-time 5 -c /tmp/pulse-book-cookies.txt http://127.0.0.1:18080/login
curl --fail --max-time 5 -b /tmp/pulse-book-cookies.txt http://127.0.0.1:18080/session
rm -f /tmp/pulse-book-cookies.txt
```

The first request returns `401` with `missing demo session`. The cookie-assisted sequence returns HTML containing `Sign in`, then JSON containing `"authenticated":true`.

Apply the journey and inspect both the source object and operator-rendered form:

```sh
kubectl --context kind-pulse-book apply -f book/examples/protocols/login-journey.yaml
kubectl --context kind-pulse-book -n book-shop wait httpcanary/catalogue-login --for=jsonpath='{.status.phase}'=Healthy --timeout=120s
kubectl --context kind-pulse-book -n book-shop get httpcanary catalogue-login -o jsonpath='{.status.lastStatus}{"\t"}{.status.message}{"\n"}'
kubectl --context kind-pulse-book -n pulse-system get configmap pulse-probe-config -o yaml
```

Expect status `200` and `Synthetic journey succeeded (2 steps)`. Locate both named steps in the ConfigMap. The CR timestamp and configuration resource version are variable.

## Fail and identify one step

Set the fixture to return a valid session response whose state is `guest`:

```sh
curl --fail --max-time 5 -H 'Content-Type: application/json' -d '{"behavior":"journey-fail"}' http://127.0.0.1:18080/__control
curl --fail --max-time 5 -c /tmp/pulse-book-cookies.txt http://127.0.0.1:18080/login
curl --fail --max-time 5 -b /tmp/pulse-book-cookies.txt http://127.0.0.1:18080/session
rm -f /tmp/pulse-book-cookies.txt
kubectl --context kind-pulse-book -n book-shop wait httpcanary/catalogue-login --for=jsonpath='{.status.phase}'=Unhealthy --timeout=120s
kubectl --context kind-pulse-book -n book-shop get httpcanary catalogue-login -o jsonpath='{.status.lastStatus}{"\t"}{.status.message}{"\n"}'
```

Pulse should report `Step 2 (read-session) failed: Response body did not contain "authenticated"`. Step numbering is one-based and stops at the first failure. A step transport error has status `0`; an assertion error retains the received status. Auth configured at `spec.auth` applies to every journey step, while each step may add its own headers and body. Per-step auth, branching, extraction, variable substitution, retries, and parallel steps are unsupported.

If a bounded wait expires, inspect the behavior and runner evidence:

```sh
curl --fail --max-time 5 http://127.0.0.1:18080/__control
kubectl --context kind-pulse-book -n pulse-system logs statefulset/pulse-probe-runner --since=5m
kubectl --context kind-pulse-book -n book-shop get httpcanary catalogue-login -o yaml
```

## Restore

```sh
curl --fail --max-time 5 -H 'Content-Type: application/json' -d '{"behavior":"healthy"}' http://127.0.0.1:18080/__control
kubectl --context kind-pulse-book -n book-shop wait httpcanary/catalogue-login --for=jsonpath='{.status.phase}'=Healthy --timeout=120s
kubectl --context kind-pulse-book -n book-shop wait httpcanary/catalogue-no-content --for=jsonpath='{.status.phase}'=Healthy --timeout=120s
kubectl --context kind-pulse-book delete -f book/examples/protocols/login-journey.yaml --ignore-not-found
kubectl --context kind-pulse-book delete -f book/examples/protocols/no-content.yaml --ignore-not-found
```

Stop the port-forward with Ctrl-C.

## Checkpoint exercise

Change the second step's marker to `guest`, apply it while the target is healthy, and predict the exact failing step and message. Verify, then restore the checked-in YAML and healthy fixture. Why does calling `/session` directly not test the same contract as the journey?
