#!/usr/bin/env python3
"""Render the live Pulse demo as an engineer-facing walkthrough.

This deliberately reads the same three surfaces the operator uses: Canary CR
status, the incident engine APIs, and AnomalyPolicy status.  It is a teaching
tool, not a second source of truth.
"""

import json
import os
import shlex
import subprocess
import sys
from datetime import datetime, timezone


KUBECTL = shlex.split(os.environ.get("KUBECTL", "kubectl"))
ENGINE_PROXY = "/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy"
STALE_SECONDS = int(os.environ.get("DEMO_STALE_SECONDS", "45"))


def kubectl_json(*args):
    command = [*KUBECTL, *args]
    try:
        completed = subprocess.run(command, check=True, capture_output=True, text=True)
    except (OSError, subprocess.CalledProcessError) as error:
        detail = getattr(error, "stderr", "").strip()
        raise SystemExit(f"Could not read the demo cluster: {detail or error}") from error
    return json.loads(completed.stdout)


def cell(value, empty="-"):
    if value is None or value == "":
        return empty
    if isinstance(value, bool):
        return str(value).lower()
    if isinstance(value, float):
        return f"{value:.3f}"
    return str(value)


def table(headers, rows):
    rows = [[cell(value) for value in row] for row in rows]
    widths = [len(header) for header in headers]
    for row in rows:
        for index, value in enumerate(row):
            widths[index] = max(widths[index], len(value))
    print("  ".join(header.ljust(widths[index]) for index, header in enumerate(headers)))
    print("  ".join("-" * width for width in widths))
    for row in rows:
        print("  ".join(value.ljust(widths[index]) for index, value in enumerate(row)))


def resources():
    items = []
    for resource, kind in (("httpcanaries", "HTTP"), ("grpccanaries", "gRPC")):
        payload = kubectl_json("-n", "shop", "get", resource, "-o", "json")
        for item in payload.get("items", []):
            item["_demoKind"] = kind
            items.append(item)
    return sorted(items, key=lambda item: item["metadata"]["name"])


def live_results():
    payload = kubectl_json("-n", "pulse-system", "get", "--raw", ENGINE_PROXY + "/results")
    if not isinstance(payload, list):
        raise SystemExit(f"Incident engine returned {type(payload).__name__} for /results; expected an array")
    return {
        result.get("name", ""): result
        for result in payload if isinstance(result, dict)
    }


def validation_for(item):
    spec = item.get("spec", {})
    if item["_demoKind"] == "gRPC":
        return f"health={spec.get('service', '<overall>')}"
    if spec.get("mcp"):
        mcp = spec["mcp"]
        return "MCP tools=" + ",".join(mcp.get("requiredTools", []))
    if spec.get("journey"):
        return f"journey={len(spec['journey'])} steps"
    checks = [f"status={spec.get('expectedStatus', 200)}"]
    if spec.get("containsText"):
        checks.append(f"contains={spec['containsText']}")
    return ", ".join(checks)


def live_state(result):
    if not result:
        return "MISSING"
    raw = result.get("lastCheckTime")
    if not raw:
        return "MISSING TIME"
    if result.get("liveAgeSeconds") is not None:
        age = max(0, int(float(result["liveAgeSeconds"])))
        label = "STALE" if age > STALE_SECONDS else "fresh"
        return f"{label} {age}s"
    try:
        checked = datetime.fromisoformat(raw.replace("Z", "+00:00"))
    except (TypeError, ValueError):
        return "INVALID TIME"
    age = max(0, int((datetime.now(timezone.utc) - checked).total_seconds()))
    label = "STALE" if age > STALE_SECONDS else "fresh"
    return f"{label} {age}s"


def detector(result, prefix):
    state = result.get(prefix + "State")
    samples = result.get(prefix + "Samples")
    score_name = "driftScore" if prefix == "drift" else "latencyZScore"
    if state == "warming":
        return f"warming ({samples or 0})"
    if state == "ready":
        return f"{float(result.get(score_name, 0)):.3f} ({samples or 0})"
    return "not evaluated"


def print_status(names=()):
    wanted = set(names)
    results = live_results()
    rows = []
    for item in resources():
        name = item["metadata"]["name"]
        if wanted and name not in wanted:
            continue
        status = item.get("status", {})
        intelligence = status.get("intelligence", {}) or {}
        result = results.get(f"shop/{name}", {})
        observed = result.get("statusCode") if "statusCode" in result else None
        persisted_check = status.get("lastCheckTime") or {}
        if isinstance(persisted_check, dict):
            persisted_check = persisted_check.get("time")
        rows.append([
            name,
            item["_demoKind"],
            validation_for(item),
            status.get("phase"),
            observed,
            detector(result, "drift"),
            detector(result, "latency"),
            intelligence.get("score"),
            intelligence.get("trigger"),
            intelligence.get("novel"),
            intelligence.get("incidentID"),
            intelligence.get("role"),
            live_state(result),
            persisted_check,
        ])
    table(
        ["CANARY", "TYPE", "VALIDATION", "CR PHASE", "LIVE CODE", "DRIFT (SAMPLES)", "LATENCY Z (SAMPLES)", "SIGNAL", "TRIGGER", "NOVEL", "INCIDENT", "ROLE", "LIVE", "CR CHECK"],
        rows,
    )
    if wanted:
        print("\nLatest probe messages (the raw result consumed by the controller):")
        for name in names:
            result = results.get(f"shop/{name}", {})
            print(f"  {name}: {cell(result.get('message'), 'no result yet')}")


def print_incidents():
    incidents = kubectl_json("-n", "pulse-system", "get", "--raw", ENGINE_PROXY + "/incidents")
    if not incidents:
        print("No open incidents. The engine closes an incident after its members recover.")
        return
    for incident in incidents:
        print(
            f"{incident.get('id')}  trigger={incident.get('trigger')}  "
            f"novel={cell(incident.get('novel'))}  root={incident.get('rootCause')}"
        )
        print(f"  signature: {incident.get('signature')}")
        for member in incident.get("members", []):
            signal = member.get("signal", {})
            score = signal.get("driftScore") or signal.get("latencyZScore")
            print(
                f"  - {member.get('probe')} [{member.get('role')}] "
                f"kind={signal.get('kind')} status={cell(signal.get('statusCode'))} "
                f"score={cell(score)} message={cell(signal.get('message'))}"
            )
        evidence = incident.get("mergeEvidence", [])
        if evidence:
            print("  merge evidence:")
            for reason in evidence:
                detail = reason.get("type", "unknown")
                if reason.get("similarity") is not None:
                    detail += (
                        f" similarity={cell(reason.get('similarity'))}"
                        f" threshold={cell(reason.get('threshold'))}"
                    )
                print(f"    {reason.get('left')} <-> {reason.get('right')}: {detail}")
        if incident.get("investigation"):
            print("  investigation:")
            for line in incident["investigation"].splitlines():
                print(f"    {line}")


def print_topology():
    topology = kubectl_json("-n", "pulse-system", "get", "--raw", ENGINE_PROXY + "/topology")
    print("Declared edges (active correlation evidence):")
    declared = topology.get("declared") or {}
    if isinstance(declared, dict):
        for canary, upstreams in sorted(declared.items()):
            for upstream in upstreams:
                print(f"  {canary} -> depends on -> {upstream}")
    elif isinstance(declared, list):
        for edge in declared:
            if isinstance(edge, list) and len(edge) == 2:
                print(f"  {edge[1]} -> depends on -> {edge[0]}")
            else:
                print(f"  {edge}")
    if not declared:
        print("  (none)")
    print("Proposed edges (learned, visible for review, not active):")
    proposals = topology.get("proposals") or []
    if not proposals:
        print("  (none yet)")
    else:
        for proposal in proposals:
            print(f"  {json.dumps(proposal, sort_keys=True)}")


def print_policy():
    policy = kubectl_json("-n", "pulse-system", "get", "anomalypolicy", "demo-triage", "-o", "json")
    status = policy.get("status", {})
    print("Policy resolution:")
    print(f"  referenced canaries: {cell(status.get('referencedBy'))}")
    print(f"  hot-path model:      {cell(status.get('resolvedHotModel'))}")
    print(f"  cold-path model:     {cell(status.get('resolvedColdModel'))}")
    for condition in status.get("conditions", []):
        print(
            f"  condition {condition.get('type')}: {condition.get('status')} "
            f"({condition.get('reason')}) {condition.get('message', '')}"
        )
    print("\nTrigger thresholds:")
    for name, config in policy.get("spec", {}).get("triggers", {}).items():
        values = ", ".join(f"{key}={value}" for key, value in config.items())
        print(f"  {name}: {values}")
    print("\nAction order (one chain per root-cause incident):")
    for index, action in enumerate(policy.get("spec", {}).get("actions", []), 1):
        print(f"  {index}. {action.get('name')} ({action.get('type')})")


def print_canary(name):
    match = next((item for item in resources() if item["metadata"]["name"] == name), None)
    if match is None:
        raise SystemExit(f"Unknown demo canary {name!r}")
    document = {
        "kind": match["kind"],
        "metadata": {"name": name, "namespace": "shop"},
        "spec": match.get("spec", {}),
        "status": match.get("status", {}),
    }
    print(json.dumps(document, indent=2))


def print_overview():
    print("""Pulse demo data flow

  HttpCanary / GrpcCanary + AnomalyPolicy
                    |
                    v  controller resolves policy + Secret refs into runtime config
          pulse-probe-runner (hot path, every check)
            |  protocol assertions: HTTP / journey / MCP / gRPC
            |  Potion body embeddings + local latency statistics
            |  raw response bodies stay here
            v
          pulse-incident-engine (cold path, only signals/failures)
            |  MiniLM similarity OR declared topology
            |  groups members, chooses root cause, scores novelty
            v
          metric -> LLM -> Slack -> observability actions
                    |
                    v
          local sink records the exact outbound payloads

The table below joins desired validation, live probe result, and model-backed
status. LIVE is classified independently from the persisted CR status: missing
or older-than-45-second results are never presented as freshly healthy. A
detector marked "warming" has not produced a trustworthy zero score yet.
""")
    print_policy()
    print("\nLive canaries:")
    print_status()
    print("\nTopology:")
    print_topology()


def usage():
    print("usage: inspect.py overview|status [canary ...]|incidents|topology|policy|canary NAME", file=sys.stderr)
    return 2


def main():
    command = sys.argv[1] if len(sys.argv) > 1 else "overview"
    if command == "overview":
        print_overview()
    elif command == "status":
        print_status(sys.argv[2:])
    elif command == "incidents":
        print_incidents()
    elif command == "topology":
        print_topology()
    elif command == "policy":
        print_policy()
    elif command == "canary" and len(sys.argv) == 3:
        print_canary(sys.argv[2])
    else:
        return usage()
    return 0


if __name__ == "__main__":
    sys.exit(main())
