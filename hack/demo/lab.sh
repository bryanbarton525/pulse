#!/usr/bin/env bash
# Discover and run the demo's validation experiments.

set -euo pipefail

validation=${1:-list}
action=${2:-show}

scenario_for() {
  case "$1" in
    status) echo no-content ;;
    content) echo content ;;
    journey) echo journey ;;
    mcp) echo mcp ;;
    grpc) echo grpc ;;
    drift) echo green-deploy ;;
    latency) echo latency ;;
    correlation) echo outage ;;
    similarity) echo similarity ;;
    novelty) echo novelty ;;
    *) return 1 ;;
  esac
}

canary_for() {
  case "$1" in
    status) echo no-content ;;
    content) echo unrelated ;;
    drift|latency) echo catalogue ;;
    journey) echo login-journey ;;
    mcp) echo mcp-tools ;;
    grpc) echo orders ;;
    correlation|novelty) echo catalogue ;;
    similarity) echo similar-a ;;
    *) return 1 ;;
  esac
}

describe() {
  case "$1" in
    status) echo 'spec.expectedStatus — require any HTTP status from 100 through 599' ;;
    content) echo 'spec.containsText — require a literal marker in a bounded response body' ;;
    journey) echo 'spec.journey[] — ordered method/status/body assertions with a shared cookie jar' ;;
    mcp) echo 'spec.mcp — protocol negotiation, capabilities, tool count, and required tool names' ;;
    grpc) echo 'spec.service — grpc.health.v1 SERVING for a named service' ;;
    drift) echo 'spec.intelligence + bodyDrift policy/override — semantic change on passing HTTP checks' ;;
    latency) echo 'spec.intelligence + latencyShift policy/override — local z-score on passing checks' ;;
    correlation) echo 'policy topology + failureCorrelation — declared edges independently provide merge evidence' ;;
    similarity) echo 'failureCorrelation.similarityThreshold — model-only merging with no declared edge' ;;
    novelty) echo 'failureNovelty.clusterThreshold — whether a recovered failure shape was seen before' ;;
    *) return 1 ;;
  esac
}

if [ "$validation" = list ]; then
  cat <<'EOF'
Hands-on validation lab

VALIDATION   CONFIGURATION SURFACE                                  DEMO
status       spec.expectedStatus                                    demo-http-contract
content      spec.containsText                                      demo-content
journey      spec.journey[]                                         demo-journey
mcp          spec.mcp                                               demo-mcp
grpc         spec.service                                           demo-grpc
drift        bodyDrift policy or per-canary override                demo-green-deploy
latency      latencyShift policy or per-canary override             demo-latency
correlation  failureCorrelation + topology.dependsOn                demo-outage
similarity   failureCorrelation.similarityThreshold                  demo-similarity
novelty      failureNovelty.clusterThreshold                        demo-novelty

Inspect one live definition:
  make demo-lab VALIDATION=status

Run its narrated, asserted experiment:
  make demo-validate VALIDATION=status

Lab 1 — change a deterministic validation and undo it:
  make demo-inspect CANARY=unrelated
  kubectl --context kind-pulse-demo -n shop patch httpcanary unrelated --type merge \
    -p '{"spec":{"containsText":"a-marker-that-is-not-present"}}'
  until kubectl --context kind-pulse-demo -n shop get httpcanary unrelated \
    -o jsonpath='{.status.message}' | grep -q 'a-marker-that-is-not-present'; do sleep 1; done
  make demo-inspect CANARY=unrelated            # observe the new assertion's failure
  make demo-incidents
  make demo-reset-definitions                   # exact reset of demo-owned CRs
  make demo-restore

Lab 2 — change a model threshold and undo it:
  kubectl --context kind-pulse-demo -n pulse-system patch anomalypolicy demo-triage --type merge \
    -p '{"spec":{"triggers":{"bodyDrift":{"threshold":"0.40"}}}}'
  make demo-lab VALIDATION=drift                # inspect effective policy
  make demo-green-deploy                        # compare evidence at the new threshold
  make demo-reset-definitions
  make demo-restore

If DEMO_CLUSTER is not pulse-demo, replace kind-pulse-demo with kind-$DEMO_CLUSTER.
Policy or canary definition changes reload runner configuration and can reset
learned detector baselines. Wait for the live detector state to say ready before
interpreting a zero score.

The inspect command shows desired spec and observed status together; the scenario
commands show the live result, model score, incident membership, and action payloads.
EOF
  exit 0
fi

scenario=$(scenario_for "$validation") || {
  echo "Unknown VALIDATION '$validation'. Run 'make demo-lab' for the list." >&2
  exit 2
}
canary=$(canary_for "$validation")

if [ "$action" = run ]; then
  exec env KUBECTL="${KUBECTL:-kubectl}" hack/demo/scenarios.sh "$scenario"
fi

printf '\n%s\n\n' "$(describe "$validation")"
KUBECTL="${KUBECTL:-kubectl}" python3 hack/demo/demo_inspect.py canary "$canary"
case "$validation" in
  drift|latency|novelty)
    printf '\nShared policy (a canary can override drift/latency tuning under spec.intelligence.overrides):\n\n'
    KUBECTL="${KUBECTL:-kubectl}" python3 hack/demo/demo_inspect.py policy
    ;;
  correlation|similarity)
    printf '\nShared policy and the engine topology it resolved:\n\n'
    KUBECTL="${KUBECTL:-kubectl}" python3 hack/demo/demo_inspect.py policy
    printf '\n'
    KUBECTL="${KUBECTL:-kubectl}" python3 hack/demo/demo_inspect.py topology
    ;;
esac
printf '\nRun it with: make demo-validate VALIDATION=%s\n' "$validation"
