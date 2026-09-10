# Write and break your first canary

Start with the previous chapter's manager and loaded target image. This exercise has no intelligence policy. The only question is whether a real HTTP response satisfies the contract you wrote.

## Deploy a target you can inspect

Read `book/examples/first-canary/target.yaml` and write or copy its three resources into your lab notes. The namespace separates the monitored application from Pulse infrastructure. The Deployment selects Pods labeled `app: book-catalogue`; the Service uses the same label so traffic reaches those Pods. Its named target port resolves to the container's port 8080.

```sh
kubectl --context kind-pulse-book apply -f book/examples/first-canary/target.yaml
kubectl --context kind-pulse-book -n book-shop rollout status deployment/catalogue --timeout=120s
kubectl --context kind-pulse-book -n book-shop get endpointslice -l kubernetes.io/service-name=catalogue
```

Expect a ready target and an endpoint address. Inspect the response independently of Pulse in another terminal:

```sh
kubectl --context kind-pulse-book -n book-shop port-forward service/catalogue 18080:8080
```

While that terminal remains open:

```sh
curl --fail --max-time 5 -i http://127.0.0.1:18080/
```

Expect HTTP 200 and JSON containing `items`. The port-forward is only for your inspection. The runner will use the Service's cluster DNS name, not your laptop's `localhost`.

## Define the contract

Open `book/examples/first-canary/canary.yaml`. Its URL includes the Service namespace, `interval: 10` schedules repeated checks, and its assertions require status 200 plus the literal text `items`. There is no model configuration in this object.

```sh
kubectl --context kind-pulse-book apply -f book/examples/first-canary/canary.yaml
kubectl --context kind-pulse-book -n pulse-system rollout status statefulset/pulse-probe-runner --timeout=180s
kubectl --context kind-pulse-book -n book-shop wait httpcanary/catalogue --for=jsonpath='{.status.phase}'=Healthy --timeout=120s
kubectl --context kind-pulse-book -n book-shop get httpcanary catalogue -o yaml
```

Expect `phase: Healthy`, `lastStatus: 200`, and a message indicating the status and response text matched. Now inspect how your definition became runtime configuration:

```sh
kubectl --context kind-pulse-book -n pulse-system get configmap pulse-probe-config -o yaml
kubectl --context kind-pulse-book -n pulse-system get statefulset pulse-probe-runner -o yaml
```

Find `book-shop/catalogue`, its URL and assertions in the configuration. Find the image, mounted configuration, and shared service account behavior in the workload. These resources are reconciled by the controller. Make contract changes to the canary, not to the rendered ConfigMap.

Mounted ConfigMap updates do not arrive instantly. In the validated lab, the controller updated configuration before the runner's next reload; the first healthy result followed that reload. A Ready runner only proves its process is ready, not that the newest canary definition has reached it. Check the reload log and the exact result message when a bounded wait takes longer than expected.

To compare persisted status with the live result, start a second port-forward:

```sh
kubectl --context kind-pulse-book -n pulse-system port-forward service/pulse-probe-runner 19091:9091
```

Then request the live view:

```sh
PULSE_INTERNAL_TOKEN=$(kubectl --context kind-pulse-book -n pulse-system get \
  secret/pulse-probe-auth -o jsonpath='{.data.internal-token}' | base64 --decode)
curl --fail --max-time 5 -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" \
  http://127.0.0.1:19091/results
unset PULSE_INTERNAL_TOKEN
```

Read the result's time and message. The CR timestamp may stay unchanged while identical healthy checks continue. That reduces API writes; it does not mean probes stopped.

## Cause a failure by changing the assertion

Ask for a marker the application does not return:

```sh
kubectl --context kind-pulse-book -n book-shop patch httpcanary catalogue --type merge \
  -p '{"spec":{"containsText":"a-marker-that-is-not-present"}}'
kubectl --context kind-pulse-book -n book-shop wait httpcanary/catalogue --for=jsonpath='{.status.phase}'=Unhealthy --timeout=120s
kubectl --context kind-pulse-book -n book-shop get httpcanary catalogue -o yaml
curl --fail --max-time 5 -i http://127.0.0.1:18080/
```

The target still answers 200. The canary is unhealthy because the explicit body assertion failed. Confirm that the status message names `a-marker-that-is-not-present`; that proves the new definition was evaluated. No incident engine or LLM is needed to explain this result.

Restore the original declaration:

```sh
kubectl --context kind-pulse-book apply -f book/examples/first-canary/canary.yaml
kubectl --context kind-pulse-book -n book-shop wait httpcanary/catalogue --for=jsonpath='{.status.phase}'=Healthy --timeout=120s
```

Stop the port-forwards with Ctrl-C in their terminals. Keep the cluster and target for later chapters. If a wait fails, inspect the runner logs and live results before changing a timeout; a missed configuration reload, network failure, and wrong assertion require different repairs.

Checkpoint: explain how the same HTTP 200 response was first healthy and then unhealthy without changing the application. Identify the source of truth for the contract and the two places where its latest result can be inspected.
