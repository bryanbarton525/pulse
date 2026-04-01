# Helm Guide

This document shows how to deploy the Pulse operator with Helm and how to create canaries once the operator is running.

## Chart Location

The generated Helm chart lives in `dist/chart`.

## Basic Install

For a public controller image and a public probe runner image:

```bash
make helm-deploy \
  IMG=ghcr.io/bryanbarton525/pulse-controller:latest \
  PROBE_RUNNER_IMAGE=ghcr.io/bryanbarton525/pulse-probe-runner:latest
```

This installs the operator into `pulse-system` by default using the Helm release name `pulse`.

## Private GHCR Install

If the repository or package is private, create a pull secret first:

```bash
token="$(gh auth token)"
kubectl create namespace pulse-system --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret docker-registry ghcr-pull-secret \
  -n pulse-system \
  --docker-server=ghcr.io \
  --docker-username=bryanbarton525 \
  --docker-password="$token" \
  --docker-email=noreply@users.noreply.github.com \
  --dry-run=client -o yaml | kubectl apply -f -
```

Then deploy with Helm:

```bash
make helm-deploy \
  IMG=ghcr.io/bryanbarton525/pulse-controller:latest \
  PROBE_RUNNER_IMAGE=ghcr.io/bryanbarton525/pulse-probe-runner:latest \
  HELM_IMAGE_PULL_SECRET=ghcr-pull-secret
```

That same secret name is passed into the controller so the probe runner Deployment it creates can also pull from private GHCR.

## Alternative Helm Commands

```bash
make helm-status
make helm-history
make helm-uninstall
```

## Create Probes

Apply one or more sample canaries:

```bash
kubectl apply -f config/samples/canary_v1alpha1_httpcanary.yaml
kubectl apply -f config/samples/canary_v1alpha1_httpcanary_204.yaml
kubectl apply -f config/samples/canary_v1alpha1_httpcanary_unhealthy.yaml
kubectl apply -f config/samples/canary_v1alpha1_httpcanary_ui_login.yaml
kubectl apply -f config/samples/canary_v1alpha1_httpcanary_api_readiness.yaml
kubectl apply -f config/samples/canary_v1alpha1_httpcanary_checkout_entry.yaml
kubectl get httpcanaries -A -w
```

The `ui_login` and `checkout_entry` samples now use scripted HTTP journeys. They support multiple requests, request bodies, headers, cookie reuse across steps, and response text matching. They are still not browser automation.

## Expected Result

- `sample-http-check` should become `Healthy`
- `sample-http-check-204` should become `Healthy`
- `sample-http-check-unhealthy` should become `Unhealthy`
- `sample-ui-login-page` should become `Healthy`
- `sample-api-readiness` should become `Healthy`
- `sample-checkout-entry` should become `Healthy`

## Synthetic Journey Shape

Use `journey` when one request is not enough:

```yaml
apiVersion: canary.iambarton.com/v1alpha1
kind: HttpCanary
metadata:
  name: sample-login-journey
spec:
  url: https://example.com/dashboard
  interval: 30
  expectedStatus: 200
  containsText: dashboard
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

Each step shares the same HTTP client and cookie jar, so session-based flows work across requests.

## Helm Example With Direct Values

If you prefer raw Helm instead of the Make target:

```bash
helm upgrade --install pulse dist/chart \
  --namespace pulse-system \
  --create-namespace \
  --set manager.image.repository=ghcr.io/bryanbarton525/pulse-controller \
  --set manager.image.tag=latest \
  --set manager.probeRunnerImage.repository=ghcr.io/bryanbarton525/pulse-probe-runner \
  --set manager.probeRunnerImage.tag=latest \
  --set manager.imagePullSecrets[0].name=ghcr-pull-secret
```
