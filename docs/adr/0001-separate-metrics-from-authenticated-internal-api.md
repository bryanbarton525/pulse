# ADR 0001: Separate metrics from the authenticated internal API

## Status

Accepted

## Context

Prometheus needs a tokenless scrape surface, while Pulse operational endpoints
expose probe results, incidents, and inferred topology. Sharing the same port
made it impossible to restrict the latter without breaking metrics collection.

## Decision

Port 9090 remains the `http` Service port for `/metrics` and `/healthz`.
Port 9091 is named `api` and serves the bearer-authenticated operational API.
The controller reads `pulse-probe-auth.data.internal-token` once per status
cycle, falling back to its preserved `auth.yaml` representation only for
existing installations. NetworkPolicies allow metrics-labelled namespaces only
to port 9090.

## Consequences

Monitoring integrations remain compatible. Operational clients must use port
9091 and send the internal bearer token; credential rotation is picked up
without coordinated restarts.
