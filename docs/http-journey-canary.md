# HTTP Journey Canary

This guide explains how to use `HttpCanary` in scripted journey mode.

Use journey mode when a single `GET` against one URL is not enough, for example:

- a login flow that sets a session cookie and then loads an authenticated page
- a checkout or cart flow that sends JSON payloads across multiple requests
- an API workflow where each step must return a specific status and response text

Pulse still executes HTTP requests only. It does not run a browser, evaluate JavaScript, or interact with DOM elements.

## When To Use It

Use a simple `HttpCanary` when one request is enough:

```yaml
apiVersion: canary.iambarton.com/v1alpha1
kind: HttpCanary
metadata:
  name: simple-api-check
spec:
  url: https://api.example.com/health
  interval: 30
  expectedStatus: 200
```

Use `journey` when health depends on multiple requests sharing the same HTTP session during one check cycle.

## Supported Fields

### Top-Level Spec

| Field | Required | Meaning |
|-------|----------|---------|
| `url` | Yes | Required by the CRD. In journey mode, use this as the canary's primary or final target because it is what `kubectl get httpcanaries` shows. |
| `interval` | No | Seconds between checks. Default is `30`. Minimum is `5`. |
| `expectedStatus` | No | Used by simple single-request mode. In journey mode, set it to match the final intended state for readability, but health is determined by the steps. |
| `method` | No | Used by simple single-request mode only. Default is `GET`. |
| `headers` | No | Used by simple single-request mode only. |
| `body` | No | Used by simple single-request mode only. |
| `containsText` | No | Used by simple single-request mode only. |
| `journey` | No | Ordered list of HTTP steps. When non-empty, Pulse executes the steps instead of the top-level request fields. |
| `outputs` | No | Telemetry sinks for the canary. Use `prometheus`, `stdout`, or both. If omitted, Pulse defaults to `prometheus`. |

### Journey Step Fields

Each entry in `journey` supports:

| Field | Required | Meaning |
|-------|----------|---------|
| `name` | Yes | Human-readable step label used in failure messages |
| `url` | Yes | Endpoint to call for this step |
| `method` | No | HTTP method. Default is `GET` |
| `headers` | No | Request headers for this step |
| `body` | No | Request body for this step |
| `expectedStatus` | No | Expected status code for this step. Default is `200` |
| `containsText` | No | Substring that must be present in the response body |

## Runtime Semantics

These details reflect the current implementation.

- If `journey` is empty, Pulse executes one HTTP request using the top-level fields.
- If `journey` is non-empty, Pulse ignores the top-level `method`, `headers`, `body`, and `containsText` during execution.
- In journey mode, Pulse executes steps in order and stops at the first failing step.
- All steps in one journey share the same HTTP client and cookie jar, so cookies set by one step are available to later steps in the same check cycle.
- A new HTTP client is created for each check cycle, so cookies do not persist across intervals.
- A step is healthy only if both conditions hold:
  - the response status equals `expectedStatus`
  - the response body contains `containsText`, when `containsText` is set
- Pulse uses the standard Go HTTP client behavior, including automatic redirect following.
- The runner uses a 10 second HTTP client timeout.
- `stdout` output emits one JSON result line per check. `prometheus` output publishes metrics for the canary on `/metrics`.

## What Shows Up In Status

- On success, a journey canary reports `Healthy` with a message like `Synthetic journey succeeded (2 steps)`.
- On failure, the message includes the failing step name, for example `Step 2 (submit-login) failed: Expected 200 but got 401`.
- `lastStatus` reflects the most recent HTTP response code observed.
- For journey canaries, set the top-level `url` to the endpoint you want humans to associate with the canary, because that is the URL shown by `kubectl get httpcanaries`.

## Example: Login Journey

This pattern is suitable for services that establish a session on one request and validate it on a later request.

```yaml
apiVersion: canary.iambarton.com/v1alpha1
kind: HttpCanary
metadata:
  name: login-journey
  namespace: default
spec:
  url: https://example.com/dashboard
  interval: 30
  expectedStatus: 200
  journey:
    - name: load-login
      url: https://example.com/login
      method: GET
      expectedStatus: 200
      containsText: Sign in
    - name: submit-login
      url: https://example.com/session
      method: POST
      headers:
        Content-Type: application/json
      body: '{"username":"demo","password":"secret"}'
      expectedStatus: 200
      containsText: dashboard
```

## Example: JSON API Workflow

This pattern is suitable for cart, checkout, and multi-step API checks.

```yaml
apiVersion: canary.iambarton.com/v1alpha1
kind: HttpCanary
metadata:
  name: checkout-journey
  namespace: default
spec:
  url: https://api.example.com/checkout
  interval: 30
  expectedStatus: 200
  journey:
    - name: create-cart
      url: https://api.example.com/cart
      method: POST
      headers:
        Content-Type: application/json
      body: '{"sku":"demo-plan","qty":1}'
      expectedStatus: 201
      containsText: demo-plan
    - name: open-checkout
      url: https://api.example.com/checkout
      method: POST
      headers:
        Content-Type: application/json
      body: '{"cart":"demo-plan"}'
      expectedStatus: 200
      containsText: checkout
```

## Authoring Guidelines

- Keep `url` aligned with the primary destination of the canary, usually the final page or API endpoint.
- Keep step names short and specific so failure messages are easy to scan.
- Put request-specific headers on the step that needs them instead of assuming inheritance.
- Use `containsText` only for stable strings. Do not match timestamps, random IDs, or localized copy that changes frequently.
- If you need browser rendering, JavaScript execution, MFA flows, or DOM interaction, this feature is not the right tool.

## Applying And Inspecting

```bash
kubectl apply -f config/samples/canary_v1alpha1_httpcanary_ui_login.yaml
kubectl apply -f config/samples/canary_v1alpha1_httpcanary_checkout_entry.yaml
kubectl get httpcanaries -A
kubectl get httpcanary sample-ui-login-page -n default -o yaml
```

## Troubleshooting

- The canary stays `Unknown`:
  - Confirm the controller and probe runner are running in `pulse-system`
  - Check `kubectl logs -n pulse-system deployment/pulse-controller-manager -c manager`
  - Check `kubectl logs -n pulse-system deployment/pulse-probe-runner -c probe-runner`
- The journey fails on a later authenticated step:
  - Verify the earlier step actually sets the session cookie your application expects
  - Verify the application does not require browser-side JavaScript before issuing the next request
- `containsText` failures are noisy:
  - Match a stable substring instead of a full response body fragment
- `kubectl get` shows a URL that was never called directly:
  - That is expected in journey mode. The top-level `url` is descriptive metadata for the canary; the step URLs drive execution.
