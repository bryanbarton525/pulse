#!/usr/bin/env python3
"""Render what the demo sink received, one section per action type.

The sink prints a JSON line per request. This turns that into something worth
reading: which actions fired, how many times, with which credentials, and what
the Slack message and language-model analysis actually said.
"""

import argparse
import collections
import json
import re
import sys


def body_of(entry):
    """Parse a recorded request body, tolerating anything unparseable.

    This is a viewer: a malformed or truncated body is worth skipping over, not
    worth crashing on and losing every other action with it.
    """
    try:
        return json.loads(entry.get("body", "") or "{}")
    except ValueError:
        return {}


def incident_ids(entry):
    """Extract exact incident IDs from each supported action payload."""
    body = body_of(entry)
    path = entry.get("path", "")
    if "chat/completions" in path:
        messages = body.get("messages", []) if isinstance(body, dict) else []
        text = "\n".join(
            message.get("content", "") for message in messages if isinstance(message, dict)
        )
        return set(re.findall(r"(?m)^# Incident ([^\s]+)$", text))
    if "slack" in path:
        text = body.get("text", "") if isinstance(body, dict) else ""
        return set(re.findall(r"Incident `([^`]+)`", text))
    records = body if isinstance(body, list) else [body]
    found = set()
    for record in records:
        if not isinstance(record, dict):
            continue
        pulse = record.get("pulse", {})
        if isinstance(pulse, dict) and pulse.get("incident"):
            found.add(str(pulse["incident"]))
        if record.get("incident"):
            found.add(str(record["incident"]))
    return found


def credential_state(entry):
    if entry.get("authPresent"):
        return f"present ({entry.get('authScheme') or 'unknown scheme'})"
    if entry.get("ddkeyPresent"):
        return "present (DD-API-KEY)"
    # Backward compatibility for logs recorded by an older demo sink. Never
    # render either value; only report its presence and scheme.
    auth = entry.get("auth", "")
    if auth:
        scheme = auth.split(None, 1)[0] if " " in auth else "present"
        return f"present ({scheme})"
    if entry.get("ddkey"):
        return "present (DD-API-KEY)"
    return "none"


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--incident", action="append", default=[], help="only render payloads for this incident ID")
    parser.add_argument("--count-llm", action="store_true", help="print only the matching LLM request count")
    parser.add_argument("--counts-json", action="store_true", help="print matching action counts as JSON")
    args = parser.parse_args()
    slack, llm, logs = [], [], []

    for line in sys.stdin:
        try:
            entry = json.loads(line)
        except ValueError:
            continue

        if args.incident and not set(args.incident).intersection(incident_ids(entry)):
            continue

        path = entry.get("path", "")
        if "slack" in path:
            slack.append(entry)
        elif "chat/completions" in path:
            llm.append(entry)
        else:
            logs.append(entry)

    if args.count_llm:
        print(len(llm))
        return 0
    if args.counts_json:
        slack_with_investigation = sum(
            1
            for entry in slack
            if "Deterministic demo response" in str(
                body_of(entry).get("text", "") if isinstance(body_of(entry), dict) else ""
            )
        )
        print(json.dumps({
            "llm": len(llm),
            "slack": len(slack),
            "slackWithInvestigation": slack_with_investigation,
            "observability": len(logs),
        }))
        return 0

    if not (slack or llm or logs):
        print("Nothing received yet. Actions fire a few seconds after an incident settles;")
        print("try `make demo-outage` first.")
        return 0

    print(f"llm calls: {len(llm)}   slack messages: {len(slack)}   log records: {len(logs)}")

    for index, entry in enumerate(llm, 1):
        print(f"\n— language model request {index} —\n  credentials: {credential_state(entry)}")
        payload = body_of(entry)
        messages = payload.get("messages", []) if isinstance(payload, dict) else []
        print("  complete prompt messages:")
        for message in messages:
            if not isinstance(message, dict):
                continue
            print(f"    [{message.get('role', 'unknown')}]")
            for row in str(message.get("content", "")).splitlines():
                print(f"      {row}")
        if entry.get("truncated"):
            print(f"  WARNING: sink truncated request body ({entry.get('bodyBytes')} bytes received)")

    if slack:
        per = collections.Counter(
            (re.search(r"Incident `([^`]+)`", body_of(e).get("text", "")) or [None, "?"])[1]
            for e in slack
        )
        print("\n— slack —")
        for incident, count in per.items():
            print(f"  {incident}: {count} message(s)")
        for index, entry in enumerate(slack, 1):
            payload = body_of(entry)
            text = payload.get("text", "") if isinstance(payload, dict) else ""
            print(f"  message {index} (credentials: {credential_state(entry)}):")
            for row in text.splitlines():
                print(f"    {row}")

    for index, entry in enumerate(logs, 1):
        print(f"\n— observability request {index} —")
        print(f"  endpoint: {entry.get('path')}")
        print(f"  credentials: {credential_state(entry)}")
        print("  payload:")
        rendered = json.dumps(body_of(entry), indent=2, sort_keys=True)
        for row in rendered.splitlines():
            print(f"    {row}")
        if entry.get("truncated"):
            print(f"  WARNING: sink truncated request body ({entry.get('bodyBytes')} bytes received)")

    return 0


if __name__ == "__main__":
    sys.exit(main())
