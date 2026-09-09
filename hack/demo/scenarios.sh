#!/usr/bin/env bash
# Deterministic, assertion-driven Pulse demo scenarios with live evidence.

set -euo pipefail

NS_APP=shop
NS_SINK=pulse-demo
read -r -a KC <<< "${KUBECTL:-kubectl}"
DEMO_TEACH=${DEMO_TEACH:-1}
DEMO_PAUSE=${DEMO_PAUSE:-0}
KUBECTL_TIMEOUT=${KUBECTL_TIMEOUT:-15s}

note() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
info() { printf '   %s\n' "$*"; }
fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
teach() { if [ "$DEMO_TEACH" = 1 ]; then printf '   \033[36m%s\033[0m\n' "$*"; fi; }
pause_demo() {
  [ "$DEMO_PAUSE" = 1 ] && [ -t 0 ] || return 0
  printf '\nPress Enter to restore and continue (Ctrl-C leaves the failure in place for inspection): '
  read -r _
}
kc() { "${KC[@]}" --request-timeout="$KUBECTL_TIMEOUT" "$@"; }

demo_intro() {
  [ "$DEMO_TEACH" = 1 ] || return 0
  kc -n pulse-system get --raw \
    '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/results' >/dev/null
  KUBECTL="${KUBECTL:-kubectl}" python3 hack/demo/demo_inspect.py overview
  printf '\nEach scenario now shows four things: the declared validation, the target mutation,\n'
  printf 'the live result/model decision, and the incident/action evidence. Assertions still\n'
  printf 'stop the run if the observed evidence does not match the explanation.\n'
}

wait_for() {
  local kind=$1 name=$2 path=$3 want=$4 timeout=${5:-300} got="" error="" deadline
  deadline=$((SECONDS + timeout))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if ! got=$(kc -n "$NS_APP" get "$kind" "$name" -o "jsonpath=$path" 2>&1); then
      error=$got
      sleep 3
      continue
    fi
    if [[ "$got" == $want ]]; then return 0; fi
    sleep 3
  done
  fail "timed out waiting for $kind/$name $path='$want' (last '$got'; read error '${error:-none}')"
}

wait_nonempty() {
  local kind=$1 name=$2 path=$3 timeout=${4:-300} got="" error="" deadline
  deadline=$((SECONDS + timeout))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if ! got=$(kc -n "$NS_APP" get "$kind" "$name" -o "jsonpath=$path" 2>&1); then
      error=$got
      sleep 3
      continue
    fi
    if [ -n "$got" ]; then return 0; fi
    sleep 3
  done
  fail "timed out waiting for a value at $kind/$name $path (read error '${error:-none}')"
}

wait_checks() {
  local kind=$1 name=$2 required=$3 timeout=${4:-300} count=0 previous="" current="" error="" deadline
  deadline=$((SECONDS + timeout))
  if ! previous=$(live_check_time "$name" 2>&1); then
    error=$previous
    previous=""
  fi
  while [ "$SECONDS" -lt "$deadline" ]; do
    # CR status is intentionally not rewritten for an unchanged healthy result;
    # use the engine's live result stream to observe every completed check.
    if ! current=$(live_check_time "$name" 2>&1); then
      error=$current
      sleep 2
      continue
    fi
    if [ -n "$current" ] && [ "$current" != "$previous" ]; then
      previous=$current
      count=$((count + 1))
      if [ "$count" -ge "$required" ]; then return 0; fi
    fi
    sleep 2
  done
  fail "observed only $count/$required fresh checks for $kind/$name (last '$current'; read error '${error:-none}')"
}

live_check_time() {
  local name=$1 payload
  payload=$(kc -n pulse-system get --raw \
    '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/results') || return
  python3 -c 'import json,sys; payload=json.load(sys.stdin); name=sys.argv[1]; assert isinstance(payload,list), "results is not an array"; print(next((r.get("lastCheckTime", "") for r in payload if isinstance(r,dict) and r.get("name") == name), ""))' \
    "$NS_APP/$name" <<< "$payload"
}

wait_result_after() {
  local name=$1 after=$2 timeout=${3:-300} checked="" error="" deadline
  deadline=$((SECONDS + timeout))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if ! checked=$(live_check_time "$name" 2>&1); then
      error=$checked
      sleep 2
      continue
    fi
    if [ -n "$checked" ]; then
      if [ -z "$after" ] || python3 -c 'from datetime import datetime; import sys; parse=lambda x: datetime.fromisoformat(x.replace("Z", "+00:00")); raise SystemExit(0 if parse(sys.argv[1]) > parse(sys.argv[2]) else 1)' "$checked" "$after"; then
        return 0
      fi
    fi
    sleep 2
  done
  fail "$name produced no fresh result after $after (last '$checked'; read error '${error:-none}')"
}

live_result_field() {
  local name=$1 field=$2 payload
  payload=$(kc -n pulse-system get --raw \
    '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/results') || return
  python3 -c 'import json,sys; payload=json.load(sys.stdin); name,field=sys.argv[1:]; assert isinstance(payload,list), "results is not an array"; print(next((r.get(field, "") for r in payload if isinstance(r,dict) and r.get("name") == name), ""))' \
    "$NS_APP/$name" "$field" <<< "$payload"
}

wait_detector_ready() {
  local name=$1 field=$2 timeout=${3:-300} state="" error="" deadline
  deadline=$((SECONDS + timeout))
  while [ "$SECONDS" -lt "$deadline" ]; do
    if ! state=$(live_result_field "$name" "$field" 2>&1); then
      error=$state
      sleep 2
      continue
    fi
    [ "$state" = ready ] && return 0
    sleep 2
  done
  fail "$name detector $field did not become ready (last '$state'; read error '${error:-none}')"
}

sink_line_count() {
  kc -n "$NS_SINK" logs deploy/sink --since=24h 2>/dev/null | wc -l | tr -d ' '
}

show_new_actions() {
  local before=$1; shift
  local previous=-1 current=-2 stable=0 waited=0 args=() incident
  : "$before" # retained for call-site compatibility; incident IDs provide exact filtering
  [ "$DEMO_TEACH" = 1 ] || return 0
  # The LLM request is logged before its response is returned. Give the rest
  # of the synchronous action chain time to arrive so the evidence is complete.
  while [ "$waited" -lt 15 ] && [ "$stable" -lt 2 ]; do
    current=$(sink_line_count)
    if [ "$current" -eq "$previous" ]; then stable=$((stable + 1)); else stable=0; fi
    previous=$current
    sleep 1
    waited=$((waited + 1))
  done
  for incident in "$@"; do args+=(--incident "$incident"); done
  note "Evidence 3/3 — outbound action payloads recorded by the local sink"
  kc -n "$NS_SINK" logs deploy/sink --since=24h 2>/dev/null \
    | python3 hack/demo/show-actions.py "${args[@]}"
  pause_demo
}

show_evidence() {
  [ "$DEMO_TEACH" = 1 ] || return 0
  note "Evidence 1/3 — desired validation joined to the latest probe result"
  KUBECTL="${KUBECTL:-kubectl}" python3 hack/demo/demo_inspect.py status "$@"
  note "Evidence 2/3 — the incident engine's current grouping and root-cause decision"
  KUBECTL="${KUBECTL:-kubectl}" python3 hack/demo/demo_inspect.py incidents
}

quiet_restore() {
  DEMO_TEACH=0 scenario_restore >/dev/null
}

prepare_scenario() {
  quiet_restore
  # Novelty clusters live in the incident engine. Reset them before an
  # independent chapter so every scenario is repeatable and can show its
  # complete action chain. The novelty chapter itself resets only here, then
  # deliberately preserves the learned shape between its two occurrences.
  kc -n pulse-system rollout restart deployment/pulse-incident-engine >/dev/null
  kc -n pulse-system rollout status deployment/pulse-incident-engine --timeout=180s >/dev/null
  wait_incidents_closed
  local settling
  settling=$(kc -n pulse-system get anomalypolicy demo-triage \
    -o jsonpath='{.spec.triggers.failureNovelty.settlingPeriodSeconds}')
  [ -n "$settling" ] || settling=0
  sleep "$((settling + 1))"
  teach "Scenario isolation: the incident engine was restarted so prior demo runs cannot suppress this chapter as already known."
}

incident_id() {
  local kind=$1 name=$2
  kc -n "$NS_APP" get "$kind" "$name" -o jsonpath='{.status.intelligence.incidentID}'
}

llm_count_for() {
  local incident=$1
  [ -n "$incident" ] || fail "cannot count LLM calls for an empty incident ID"
  kc -n "$NS_SINK" logs deploy/sink --since=24h 2>/dev/null \
    | python3 hack/demo/show-actions.py --incident "$incident" --count-llm
}

action_counts_for() {
  local incident=$1
  [ -n "$incident" ] || fail "cannot count actions for an empty incident ID"
  kc -n "$NS_SINK" logs deploy/sink --since=24h 2>/dev/null \
    | python3 hack/demo/show-actions.py --incident "$incident" --counts-json \
    | python3 -c 'import json,sys; x=json.load(sys.stdin); print(x["llm"], x["slack"], x["observability"], x["slackWithInvestigation"])'
}

assert_actions() {
  local incident=$1 want_llm=${2:-1} timeout=${3:-120} counts="0 0 0 0" llm=0 slack=0 observability=0 investigated=0 want_investigated=0 deadline
  deadline=$((SECONDS + timeout))
  [ "$want_llm" -eq 0 ] || want_investigated=1
  while [ "$SECONDS" -lt "$deadline" ]; do
    counts=$(action_counts_for "$incident")
    read -r llm slack observability investigated <<< "$counts"
    if [ "$llm" -gt "$want_llm" ] || [ "$slack" -gt 1 ] || [ "$observability" -gt 1 ]; then
      fail "$incident produced duplicate actions (llm=$llm slack=$slack observability=$observability)"
    fi
    if [ "$llm" -eq "$want_llm" ] && [ "$slack" -eq 1 ] && [ "$observability" -eq 1 ] && [ "$investigated" -eq "$want_investigated" ]; then
      # Keep a short duplicate-observation window after the complete chain.
      sleep 3
      counts=$(action_counts_for "$incident")
      [ "$counts" = "$want_llm 1 1 $want_investigated" ] \
        || fail "$incident action counts changed during duplicate window: $counts"
      return 0
    fi
    sleep 2
  done
  fail "$incident action chain incomplete (llm=$llm/$want_llm slack=$slack/1 observability=$observability/1 slack-investigation=$investigated/$want_investigated)"
}

wait_llm_for() {
  local incident=$1 timeout=${2:-120} got=0 deadline
  deadline=$((SECONDS + timeout))
  while [ "$SECONDS" -lt "$deadline" ]; do
    got=$(llm_count_for "$incident")
    if [ "$got" -ge 1 ]; then return 0; fi
    sleep 2
  done
  fail "expected one simulated LLM call for $incident, saw $got"
}

assert_one_llm() {
  assert_actions "$1" 1
}

set_behavior() {
	local service=$1 behavior=$2 response
	response=$(kc create --raw "/api/v1/namespaces/$NS_APP/services/http:$service:8080/proxy/__control" -f - \
		<<< "{\"behavior\":\"$behavior\"}")
	python3 -c 'import json,sys; payload=json.load(sys.stdin); expected=sys.argv[1]; assert payload.get("behavior") == expected, payload' \
		"$behavior" <<< "$response"
}

set_behaviors() {
	local behavior=$1; shift
	local service
	for service in "$@"; do set_behavior "$service" "$behavior"; done
}

wait_all_healthy() {
  local name
  for name in catalogue checkout search no-content login-journey mcp-tools similar-a similar-b unrelated; do
    wait_for httpcanary "$name" '{.status.phase}' Healthy
  done
  wait_for grpccanary orders '{.status.phase}' Healthy
}

wait_incidents_closed() {
  local name trigger
  for name in catalogue checkout search no-content login-journey mcp-tools similar-a similar-b unrelated; do
    wait_for httpcanary "$name" '{.status.intelligence.incidentID}' ''
  done
  wait_for grpccanary orders '{.status.intelligence.incidentID}' ''
}

warm_drift_and_latency_baselines() {
  wait_all_healthy
  info "waiting until the runner reports the live drift and latency baselines ready"
  wait_detector_ready catalogue driftState
  wait_detector_ready catalogue latencyState
  wait_detector_ready checkout latencyState
  wait_detector_ready search latencyState
}

scenario_restore() {
  note "restore — all targets and canaries healthy"
  local names=(catalogue checkout search no-content login-journey mcp-tools similar-a similar-b unrelated orders)
  local baselines=() name index
  for name in "${names[@]}"; do baselines+=("$(live_check_time "$name" 2>/dev/null || true)"); done
  set_behaviors healthy catalogue checkout search unrelated mcp orders-grpc
  for index in "${!names[@]}"; do
    wait_result_after "${names[$index]}" "${baselines[$index]}"
  done
  wait_all_healthy
  wait_incidents_closed
  info "all ten canaries are healthy and no incident remains open"
}

scenario_green_deploy() {
  note "green deploy — Potion catches semantic drift while HTTP stays green"
  teach "Validation: expectedStatus=200 and containsText=items; intelligence also learns the healthy body shape."
  teach "Mutation: catalogue still returns 200 and still contains 'items', but changes from two products to an empty list."
  teach "Under the hood: the runner embeds each passing body locally with Potion; only a drift score leaves that pod."
  prepare_scenario
  warm_drift_and_latency_baselines
  local incident before_lines
  before_lines=$(sink_line_count)
  set_behavior catalogue green
  wait_for httpcanary catalogue '{.status.intelligence.trigger}' bodyDrift
  wait_for httpcanary catalogue '{.status.phase}' Healthy
  wait_nonempty httpcanary catalogue '{.status.intelligence.incidentID}'
  incident=$(incident_id httpcanary catalogue)
  assert_one_llm "$incident"
  show_evidence catalogue
  show_new_actions "$before_lines" "$incident"
  info "verified: status stayed Healthy, trigger=bodyDrift, exactly one investigation"
}

scenario_latency() {
  note "latency shift — passing responses become materially slower"
  teach "Validation: all three endpoints must still return their expected HTTP status."
  teach "Mutation: catalogue sleeps for two seconds; checkout and search make real calls to it and inherit the delay."
  teach "Under the hood: local EWMA statistics detect repeated z-score breaches; no embedding model is needed."
  teach "Each canary owns its baseline, so this produces three passing latency incidents; failure correlation is demonstrated separately."
  prepare_scenario
  warm_drift_and_latency_baselines
  local catalogue_id checkout_id search_id before_lines
  before_lines=$(sink_line_count)
  set_behavior catalogue slow
  wait_for httpcanary catalogue '{.status.intelligence.trigger}' latencyShift 360
  wait_for httpcanary checkout '{.status.intelligence.trigger}' latencyShift 360
  wait_for httpcanary search '{.status.intelligence.trigger}' latencyShift 360
  wait_for httpcanary catalogue '{.status.phase}' Healthy
  catalogue_id=$(incident_id httpcanary catalogue)
  checkout_id=$(incident_id httpcanary checkout)
  search_id=$(incident_id httpcanary search)
  assert_one_llm "$catalogue_id"
  assert_one_llm "$checkout_id"
  assert_one_llm "$search_id"
  show_evidence catalogue checkout search
  show_new_actions "$before_lines" "$catalogue_id" "$checkout_id" "$search_id"
  info "verified: all checks stayed Healthy and latency propagated to both real callers"
}

scenario_no_content() {
  note "HTTP contract — expected 204 changes to 200"
  teach "Validation: the no-content canary explicitly requires HTTP 204, proving success is not hard-coded to 200."
  teach "Mutation: the handler returns HTTP 200 plus an unexpected body."
  prepare_scenario
  local incident before_lines
  before_lines=$(sink_line_count)
  set_behavior catalogue no-content-fail
  wait_for httpcanary no-content '{.status.phase}' Unhealthy
  wait_nonempty httpcanary no-content '{.status.intelligence.incidentID}'
  incident=$(incident_id httpcanary no-content)
  assert_one_llm "$incident"
  show_evidence no-content
  show_new_actions "$before_lines" "$incident"
  info "verified: the non-200-default HTTP canary opened exactly one incident"
}

scenario_content() {
  note "HTTP content contract — status stays 200 but a required marker disappears"
  teach "Validation: the unrelated canary requires both HTTP 200 and the literal marker 'healthy-control'."
  teach "Mutation: the endpoint keeps returning 200 but changes that marker to 'degraded-control'."
  teach "This is deterministic containsText validation, separate from Potion's semantic body-drift detection."
  prepare_scenario
  local incident before_lines
  before_lines=$(sink_line_count)
  set_behavior unrelated control-fail
  wait_for httpcanary unrelated '{.status.phase}' Unhealthy
  wait_nonempty httpcanary unrelated '{.status.intelligence.incidentID}'
  incident=$(incident_id httpcanary unrelated)
  assert_one_llm "$incident"
  show_evidence unrelated
  show_new_actions "$before_lines" "$incident"
  info "verified: HTTP stayed 200, containsText failed, and one incident was investigated"
}

scenario_journey() {
  note "journey — the session step violates its assertion"
  teach "Validation: GET /login must contain 'Sign in', then GET /session must contain 'authenticated'."
  teach "Mutation: only step two changes, returning a guest session without the required marker."
  teach "Under the hood: one client and cookie jar are retained across the ordered journey steps."
  prepare_scenario
  local incident before_lines
  before_lines=$(sink_line_count)
  set_behavior catalogue journey-fail
  wait_for httpcanary login-journey '{.status.phase}' Unhealthy
  wait_nonempty httpcanary login-journey '{.status.intelligence.incidentID}'
  incident=$(incident_id httpcanary login-journey)
  assert_one_llm "$incident"
  show_evidence login-journey
  show_new_actions "$before_lines" "$incident"
  info "verified: the second journey step failed and opened exactly one incident"
}

scenario_mcp() {
  note "MCP — tools/list loses a required tool"
  teach "Validation: initialize must negotiate the configured protocol, advertise tools, and tools/list must include health.check."
  teach "Mutation: the MCP server remains reachable but removes health.check from its registry."
  teach "Under the hood: the runner performs initialize -> notifications/initialized -> tools/list, not a shallow HTTP ping."
  prepare_scenario
  local incident before_lines
  before_lines=$(sink_line_count)
  set_behavior mcp mcp-missing-tool
  wait_for httpcanary mcp-tools '{.status.phase}' Unhealthy
  wait_nonempty httpcanary mcp-tools '{.status.intelligence.incidentID}'
  incident=$(incident_id httpcanary mcp-tools)
  assert_one_llm "$incident"
  show_evidence mcp-tools
  show_new_actions "$before_lines" "$incident"
  info "verified: health.check disappeared and exactly one incident was investigated"
}

scenario_grpc() {
  note "gRPC — standard health service reports NOT_SERVING"
  teach "Validation: grpc.health.v1 must report SERVING for the shop.Orders service."
  teach "Mutation: transport remains available but the health service reports NOT_SERVING."
  prepare_scenario
  local incident before_lines
  before_lines=$(sink_line_count)
  set_behavior orders-grpc grpc-fail
  wait_for grpccanary orders '{.status.phase}' Unhealthy
  wait_nonempty grpccanary orders '{.status.intelligence.incidentID}'
  incident=$(incident_id grpccanary orders)
  assert_one_llm "$incident"
  show_evidence orders
  show_new_actions "$before_lines" "$incident"
  info "verified: gRPC health failed and exactly one incident was investigated"
}

trigger_outage() {
  set_behaviors outage catalogue
}

wait_correlated_outage() {
  wait_for httpcanary catalogue '{.status.phase}' Unhealthy
  wait_for httpcanary checkout '{.status.phase}' Unhealthy
  wait_for httpcanary search '{.status.phase}' Unhealthy
  wait_nonempty httpcanary catalogue '{.status.intelligence.incidentID}'
  wait_for httpcanary catalogue '{.status.intelligence.role}' rootCause
  wait_for httpcanary checkout '{.status.intelligence.role}' downstream
  wait_for httpcanary search '{.status.intelligence.role}' downstream
  local deadline=$((SECONDS + 120)) catalogue_id checkout_id search_id
  while [ "$SECONDS" -lt "$deadline" ]; do
    catalogue_id=$(incident_id httpcanary catalogue)
    checkout_id=$(incident_id httpcanary checkout)
    search_id=$(incident_id httpcanary search)
    if [ -n "$catalogue_id" ] && [ "$catalogue_id" = "$checkout_id" ] && [ "$catalogue_id" = "$search_id" ]; then
      return 0
    fi
    sleep 2
  done
  fail "catalogue, checkout, and search did not converge on one incident (catalogue=$catalogue_id checkout=$checkout_id search=$search_id)"
}

scenario_outage() {
  note "correlated outage — one real upstream failure, two downstream victims"
  teach "Validation: catalogue, checkout, search, and the unrelated control each have independent contracts."
  teach "Mutation: catalogue returns 529; its two real callers surface 503. The control fails differently at the same time."
  teach "Under the hood: declared topology OR sufficient MiniLM similarity can merge failures. This chapter proves the declared-edge path; timing alone cannot merge the control."
  prepare_scenario
  local correlated_id unrelated_id before_lines
  before_lines=$(sink_line_count)
	set_behavior catalogue outage
	set_behavior unrelated control-fail
  wait_correlated_outage
  wait_for httpcanary unrelated '{.status.phase}' Unhealthy
  correlated_id=$(incident_id httpcanary catalogue)
  unrelated_id=$(incident_id httpcanary unrelated)
  [ -n "$unrelated_id" ] && [ "$unrelated_id" != "$correlated_id" ] \
    || fail "negative control was incorrectly merged into the catalogue incident"
  assert_one_llm "$correlated_id"
  assert_one_llm "$unrelated_id"
  show_evidence catalogue checkout search unrelated
  show_new_actions "$before_lines" "$correlated_id" "$unrelated_id"
  info "verified: one 3-member incident, catalogue root cause, separate negative control"
}

scenario_similarity() {
  note "similarity-only correlation — model evidence without declared topology"
  teach "Validation: similar-a and similar-b are separate canaries with no dependency edge; orders is a simultaneous negative control."
  teach "Mutation: the pair receives the same content assertion failure while gRPC independently reports NOT_SERVING."
  teach "Under the hood: identical MiniLM vectors exceed the correlation threshold. The unrelated gRPC failure remains separate."
  prepare_scenario
  local pair_id control_id before_lines
  before_lines=$(sink_line_count)
	set_behavior unrelated similarity-fail
	set_behavior orders-grpc grpc-fail
  wait_for httpcanary similar-a '{.status.phase}' Unhealthy
  wait_for httpcanary similar-b '{.status.phase}' Unhealthy
  wait_for grpccanary orders '{.status.phase}' Unhealthy
  wait_nonempty httpcanary similar-a '{.status.intelligence.incidentID}'
  wait_nonempty httpcanary similar-b '{.status.intelligence.incidentID}'
  wait_nonempty grpccanary orders '{.status.intelligence.incidentID}'
  local deadline=$((SECONDS + 120)) a_id b_id
  while [ "$SECONDS" -lt "$deadline" ]; do
    a_id=$(incident_id httpcanary similar-a)
    b_id=$(incident_id httpcanary similar-b)
    [ -n "$a_id" ] && [ "$a_id" = "$b_id" ] && break
    sleep 2
  done
  [ -n "${a_id:-}" ] && [ "$a_id" = "${b_id:-}" ] \
    || fail "similar-a and similar-b did not converge on one incident"
  pair_id=$a_id
  control_id=$(incident_id grpccanary orders)
  [ "$control_id" != "$pair_id" ] || fail "dissimilar gRPC control merged into similarity pair"
  assert_one_llm "$pair_id"
  assert_one_llm "$control_id"
  show_evidence similar-a similar-b orders
  show_new_actions "$before_lines" "$pair_id" "$control_id"
  info "verified: similarity evidence merged the pair without topology; the dissimilar control stayed separate"
}

scenario_novelty() {
  note "novelty — a known outage skips a second expensive investigation"
  teach "Validation: replay the identical failure shape after recovery. The incident may reopen, but its novelty must be false."
  teach "Under the hood: MiniLM clusters the root failure signature; novelty gates only expensive investigation, not monitoring."
  prepare_scenario
  local first_id second_id novel before_lines
  before_lines=$(sink_line_count)
  trigger_outage
  wait_correlated_outage
  wait_nonempty httpcanary catalogue '{.status.intelligence.novel}'
  novel=$(kc -n "$NS_APP" get httpcanary catalogue -o jsonpath='{.status.intelligence.novel}')
  first_id=$(incident_id httpcanary catalogue)
  [ "$novel" = true ] || fail "first isolated failure $first_id was not classified as novel"
  assert_actions "$first_id" 1
  quiet_restore
  wait_checks httpcanary catalogue 2
  trigger_outage
  wait_correlated_outage
  wait_for httpcanary catalogue '{.status.intelligence.novel}' false
  wait_checks httpcanary catalogue 2
  second_id=$(incident_id httpcanary catalogue)
  [ "$second_id" != "$first_id" ] || fail "replay did not open a distinct incident"
  assert_actions "$second_id" 0
  show_evidence catalogue checkout search
  show_new_actions "$before_lines" "$first_id" "$second_id"
  info "verified: novel=false skipped only the simulated LLM; Slack and observability still received one notification"
}

case "${1:-all}" in
  ready) wait_all_healthy ;;
  restore) scenario_restore ;;
  green-deploy) scenario_green_deploy ;;
  latency) scenario_latency ;;
  no-content) scenario_no_content ;;
  content) scenario_content ;;
  journey) scenario_journey ;;
  mcp) scenario_mcp ;;
  grpc) scenario_grpc ;;
  outage) scenario_outage ;;
  similarity) scenario_similarity ;;
  novelty) scenario_novelty ;;
  all)
    demo_intro
    scenario_green_deploy
    scenario_latency
    scenario_no_content
    scenario_content
    scenario_journey
    scenario_mcp
    scenario_grpc
    scenario_outage
    scenario_similarity
    scenario_novelty
    scenario_restore
    ;;
  *) fail "unknown scenario: $1" ;;
esac
