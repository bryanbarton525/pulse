# Pulse Final Review Remediation Plan

## Status and baseline

- Reviewed baseline: `main` at `29aaedfb7011d403f993cdc0dbb9de0b1c69fc6f`.
- Active remediation branch: `codex/final-review-fixes`.
- Existing checkpoints to preserve:
  - `c93f160` — Graphify review artifacts.
  - `206fb62` — initial remote embedding response validation.
- Delivery: one staged remediation PR with independently reviewable commits.
- Dependency upgrades: separate PRs after the remediation PR lands.
- Source findings: [`findings.md`](findings.md).

## Outcome

Resolve findings F01-F06, prepare evidence-driven closure of F07, and correct the additional status-sync coupling discovered during the docs-driven review. The completed work must leave Pulse releasable, protect operational APIs without breaking metrics integrations, keep model state internally consistent, project current status accurately, and teach the new operational workflow consistently throughout the demos and documentation.

## 1. Finish embedding hardening and model-swap safety

- Complete regression coverage for invalid indexes, missing indexes, empty vectors, mixed dimensions, NaN/infinity, dimension changes, and concurrent first requests.
- Give every cold embedder an internal space identity derived from a SHA-256 fingerprint of its resolved configuration:
  - HTTP: backend, normalized endpoint, and model name.
  - ONNX: backend, model path, vocabulary path, and maximum sequence length.
  - Exclude API keys and credential identifiers.
- Wrap or configure HTTP and ONNX embedders so every returned vector carries that identity instead of generic `minilm`.
- Change `Engine.SetEmbedder` semantics:
  - Different space identity: clear the correlation candidate window and novelty index.
  - Same identity, including credential rotation: replace the embedder without discarding learned state.
  - Retain open incidents and model-independent topology inference in both cases.
  - Preserve the existing wait for in-flight embedding operations.
- Keep strict cosine panics for internal invariant violations. External embedder failures must return errors and degrade to topology-only correlation.

## 2. Correct status and proposal lifecycle

- Split each StatusSyncer cycle into independent result, incident, and proposal phases. Failure of one HTTP surface must not prevent the others from synchronizing.
- Treat every successful empty response as authoritative:
  - Empty incidents clear obsolete canary intelligence status.
  - Empty proposals clear obsolete `inferredDependencies`.
  - Empty results perform no phase updates but do not stop incident/proposal processing.
- Treat transport, authentication, and decoding failures as unknown state: log them and retain the last successfully projected status.
- When an `AnomalyPolicy` becomes unreferenced, clear its inferred dependencies during policy reconciliation even if the optional engine has already been removed.
- Re-fetch status objects and retry conflicts before applying status patches, preserving fields owned by the other status writer.
- Define `lastSignalTime` as the latest signal received for the current incident, projected to Kubernetes status at most once per minute:
  - Update immediately for a new incident, role, trigger, score, novelty, investigation change, or incident closure.
  - For timestamp-only changes, update when the new incident timestamp is at least one minute newer.
  - Compare signal timestamps rather than wall-clock time so tests remain deterministic.
- Update the API field documentation and regenerate manifests through the repository's supported generation targets.

## 3. Separate and authenticate runtime APIs

Use the selected compatibility boundary:

- Port `9090` remains the metrics and liveness port.
- New port `9091`, named `api`, serves operational APIs.
- Retain the existing `--listen` flag for metrics and liveness and add `--api-listen` for operational traffic.
- Keep the legacy Service port name `http` on port 9090 for monitoring compatibility; add Service port `api` on 9091.
- Apply the same layout to the runner StatefulSet, runner Services, and incident-engine Deployment and Service.

Routing and authorization:

- Runner API on 9091: authenticate `GET /results`.
- Engine API on 9091: authenticate `POST /observations`, `POST /results`, `GET /results`, `GET /incidents`, and `GET /topology`.
- Metrics and `/healthz` remain unauthenticated at the application layer on 9090, with access constrained by NetworkPolicy.
- Preserve tokenless behavior only when a binary is intentionally started without an internal token for local development.

Token distribution:

- Add `internal-token` as a dedicated data key in the operator-owned `pulse-probe-auth` Secret while retaining `auth.yaml`.
- Preserve existing installations by reading the token from `internal-token`, falling back to the reserved value in `auth.yaml`, and then reconciling both representations atomically.
- Have the production StatusSyncer read the dedicated Secret key once per cycle and attach `Authorization: Bearer ...` to every operational request.
- Keep the token provider injectable so unit tests can use empty, static, erroneous, and rotating sources.
- Never fall back to an unauthenticated production request when the Secret cannot be read.
- Update NetworkPolicies:
  - Controller to runner API on 9091.
  - Controller and runners to engine API on 9091.
  - Namespaces labeled `metrics: enabled` to metrics only on 9090.
- Regenerate the installer after manifest changes. Do not hand-edit generated CRDs, RBAC, or installer output.

## 4. Restore GHCR publishing

- In GitHub package settings, grant the Pulse repository Actions access to:
  - `pulse-controller`
  - `pulse-probe-runner`
  - `pulse-incident-engine`
- Standardize the workflow on repository-scoped `GITHUB_TOKEN`. Remove automatic preference for a configured PAT; document a PAT only as a manual break-glass option.
- Add a preflight before model downloads and image builds:
  - Request registry access for each repository.
  - Query the GHCR tags or catalog surface.
  - Accept successful access and an explicit package or manifest-not-found response for first publication.
  - Fail immediately on unauthorized, forbidden, or denied responses.
  - Never print credentials or registry tokens.
- After package linkage is corrected, run the workflow and verify all three manifests contain `linux/amd64` and `linux/arm64`.
- Validate release-asset creation on the next intended version tag.

## 5. Update demos, book, architecture, and project governance

- Replace operational `kubectl get --raw` Service-proxy examples with an authenticated port-forward workflow:
  - Read only `.data.internal-token` into a shell variable with tracing disabled.
  - Port-forward the engine or runner API port 9091.
  - Send the bearer token with `curl`.
  - Unset the token after inspection.
- Keep metrics examples on port 9090 without the internal bearer token.
- Refactor the demo inspection and scenario helper to obtain the dedicated token key and manage an authenticated API port-forward without displaying the token.
- Update the quick start, deep-dive book, operations guide, standalone HTML documentation, diagrams, and Mermaid flows so ports, trust boundaries, token rotation, model identity, proposal expiry, and `lastSignalTime` semantics agree with the code.
- Add a root `CONTEXT.md` defining Pulse-specific terms: operational API, metrics endpoint, internal token, embedding-space identity, inferred dependency, declared dependency, and last signal time.
- Add `docs/adr/0001-separate-metrics-from-authenticated-internal-api.md`, recording why metrics remain on 9090 while authenticated operational traffic moves to 9091.
- Update this file as each checkpoint completes. Move obsolete Pulse Book branch and PR details to git history rather than restoring them here.
- Update `findings.md` with resolution commits and validation evidence rather than deleting its historical findings.
- Rebuild Graphify after the final code and documentation changes. Commit only its small report, manifest, and cost artifacts; keep large HTML, JSON, and caches ignored.

## 6. Handle dependency alerts separately

- Re-authenticate GitHub CLI and retrieve all open Dependabot alert details before changing dependencies.
- Record advisory, package, affected component, reachable surface, fixed version, and compatibility risk.
- Use one PR per existing Dependabot branch or cohesive dependency family. Do not combine these upgrades with the remediation PR.
- Process upgrades by descending severity, then runtime reachability, then smallest compatibility risk.
- Rebase or regenerate each dependency branch from the latest `main`; do not merge conflicting independent `go.mod` snapshots together.
- For every alert, either merge a validated upgrade, document a justified dismissal, or assign an owner and deadline.

## Public and internal interface changes

- Runtime Services gain authenticated operational port `9091` named `api`.
- Port `9090` and legacy Service port name `http` continue to expose metrics and liveness.
- Runtime binaries gain `--api-listen`, defaulting to `:9091`; `--listen` remains metrics and liveness and defaults to `:9090`.
- `pulse-probe-auth` gains the internal data key `internal-token`.
- All operational runner and engine endpoints require the internal bearer token in production.
- No new CRD spec fields are introduced.
- `lastSignalTime` receives a documented, rate-limited status contract.
- Embedding-space fingerprints remain internal and do not expose credentials or change the AnomalyPolicy schema.

## Commit and review sequence

1. Preserve existing commits `c93f160` and `206fb62`.
2. Add embedding identity, swap semantics, and remaining tests.
3. Add status lifecycle, timestamp cadence, and conflict-safe updates.
4. Add API and metrics separation, authentication, Secret migration, and policies.
5. Add the GHCR preflight workflow.
6. Add documentation, CONTEXT, ADR, plan and findings updates, and refreshed Graphify artifacts.
7. Open one remediation PR and keep commits unsquashed during review.
8. Open dependency PRs separately after the remediation PR establishes the new baseline.

## Test plan

### Unit and race tests

- Every malformed remote embedding response.
- Concurrent dimension discovery.
- Same-space credential rotation versus different-space model replacement.
- Candidate and novelty reset with open-incident retention.
- Missing, incorrect, correct, and rotated bearer tokens.
- API and metrics mux separation.
- Empty, error, and partial proposal and incident responses.
- Timestamp-only updates below and above one minute.
- Immediate material changes and incident closure.
- Status conflict retries and preservation of independently owned fields.

### Controller and envtest

- Existing Secret migration creates matching `auth.yaml` and `internal-token` values.
- Services, workloads, named ports, arguments, and NetworkPolicies match the new boundary.
- Sharded runner fallback authenticates every replica request.

### Isolated Kind E2E

- Deterministic HTTP and gRPC status still synchronize.
- Runner writes and controller reads authenticate successfully.
- Missing and incorrect tokens receive 401 through a port-forward.
- Metrics collectors reach 9090 but cannot reach operational port 9091.
- Deleting and regenerating the auth Secret causes temporary retries and automatic recovery without coordinated restarts.
- Empty proposal and incident responses clear projected status.
- Bad embedding responses degrade to topology-only operation without restarting the engine.
- The full demo tour, reset path, and manual book exercises produce the documented evidence.

### Full validation

Run at minimum:

```sh
go test ./internal/embed ./internal/incident ./internal/controller
go test -race ./internal/embed ./internal/incident ./internal/controller
make test
make lint
npm ci --prefix book --ignore-scripts
npm run --prefix book assets
mdbook build
python3 book/check-links.py
```

Also run:

- Manifest and installer regeneration checks.
- The isolated Kind E2E suite using an explicit dedicated context.
- Pre-commit hooks with no newly tracked large files.
- Graphify architecture and authentication queries after rebuilding the graph.
- GHCR workflow and multi-architecture manifest inspection after package permissions are corrected.

## Acceptance criteria

- Malformed or changing remote embeddings cannot panic the incident engine.
- A true model-space change clears only vector-dependent transient state; credential rotation does not.
- Successful empty engine responses clear obsolete incident and proposal status.
- Failures on one status source do not freeze other independently available status surfaces.
- `lastSignalTime` matches its documented one-minute projection contract.
- Operational APIs reject missing and incorrect tokens in production.
- Metrics remain scrapeable on port 9090 without granting incident-data access.
- Token rotation recovers automatically without coordinated component restarts.
- The controller continues to synchronize single-runner and sharded results.
- The publish workflow rejects missing GHCR permission before expensive work and successfully publishes all three architectures after access is granted.
- The demo, quick start, deep-dive guide, operations guide, diagrams, and generated site agree with runtime behavior.
- All tests, lint, documentation validation, E2E checks, pre-commit hooks, and final Graphify review pass.

## Assumptions and fixed decisions

- The embedding implementation on `206fb62` is retained and extended, not rewritten.
- Already-open incidents survive model changes; only vector-dependent candidate and novelty state resets.
- Credential rotation does not constitute an embedding-space change.
- Changing a model in place behind an unchanged endpoint and model identifier is outside automatic detection; operators must change configured identity or restart the engine.
- The operator-managed auth Secret is an internal implementation contract, so adding a dedicated key is backward-compatible.
- `GITHUB_TOKEN` is the standard GHCR credential; PAT use is break-glass only.
- GitHub package Actions access is an external repository-owner action and cannot be solved solely in code.
- Non-dependency findings are delivered in one staged remediation PR.
- Dependency upgrades remain outside the remediation PR.
