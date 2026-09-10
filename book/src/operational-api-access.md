# Access authenticated operational APIs

Pulse exposes metrics and liveness without application-layer authentication on port 9090. Results, incidents, and topology are operational data and are available only on port 9091 with the controller-owned Bearer token.

For the commands in this book, start a dedicated port-forward and read the token without printing it:

```sh
kubectl --context kind-pulse-book -n pulse-system port-forward \
  service/pulse-incident-engine 19091:9091 >/tmp/pulse-engine-forward.log 2>&1 &
PULSE_ENGINE_FORWARD_PID=$!
PULSE_INTERNAL_TOKEN=$(kubectl --context kind-pulse-book -n pulse-system get \
  secret/pulse-probe-auth -o jsonpath='{.data.internal-token}' | base64 --decode)
```

Authenticated reads then use:

```sh
curl --fail --max-time 5 \
  -H "Authorization: Bearer $PULSE_INTERNAL_TOKEN" \
  http://127.0.0.1:19091/results | python3 -m json.tool
```

When inspection is complete, clean up even if an earlier command failed:

```sh
kill "$PULSE_ENGINE_FORWARD_PID" 2>/dev/null || true
wait "$PULSE_ENGINE_FORWARD_PID" 2>/dev/null || true
unset PULSE_INTERNAL_TOKEN PULSE_ENGINE_FORWARD_PID
```

Never paste the token into logs, shell tracing, screenshots, or command output. A rotated Secret is picked up asynchronously by the controller, runners, and engine; transient HTTP 401 responses are expected only during the bounded convergence window.
