# Pulse final review findings and remediation plan

## Purpose

This document is the plan-mode handoff for the final review of Pulse after PR #4 merged into `main`. It records the exact reviewed revision, evidence, completed work, remaining findings, implementation order, and acceptance tests. It is intentionally actionable without requiring the original Codex conversation.

## Repository state

- Reviewed baseline: `main` at `29aaedfb7011d403f993cdc0dbb9de0b1c69fc6f`.
- Active delivery branch: `cursor/cloud-agent-1789008034228-6x55c`, fast-forwarded to PR #13 head `b5bf70d` before the completion work.
- First pushed checkpoint: `c93f160` (`docs: add Graphify review artifacts`).
- Graphify report, manifest, and cost metadata are tracked under `graphify-out/`.
- `graphify-out/graph.json`, `graphify-out/graph.html`, and extraction caches remain ignored because they are reproducible and exceed the repository's large-file hook threshold.
- Do not edit generated CRDs, generated RBAC, `zz_generated.*.go`, or `PROJECT` directly.

## PR #13 completion evidence

The current working tree closes the remaining repository-owned portions of F02, F04, F05, and F06 and preserves the completed F03 hardening:

- F02: `internal/authn`, both runtime server packages, and binary wiring now require a structurally valid Bearer header, fail closed for an empty production token, support an explicit local-only bypass, and keep metrics/liveness isolated on 9090 from operational APIs on 9091. Server and demo-helper regressions cover malformed headers, rotation, closed-empty-token behavior, redaction, and port-forward cleanup.
- F04: successful empty proposal responses remain authoritative, and policy reconciliation clears inferred dependencies after the final canary reference disappears, even when the optional engine is absent.
- F05: status writers re-fetch and retry conflicts while preserving independently owned fields. `lastSignalTime` is documented as the latest signal for the active incident; timestamp-only writes use the tested one-minute boundary, while material changes and closure remain immediate.
- F06: `plan.md` is replaced by the current PR #13 completion plan and was deliberately preserved while moving from the base branch to the delivery branch.
- Generated CRDs and `dist/install.yaml` were rebuilt with `make manifests generate` and `make build-installer`; they were not edited by hand.
- `make test` passed with coverage; `make lint-fix` reported zero issues; `go test -race ./internal/embed ./internal/incident ./internal/controller` passed; demo helper tests passed; and mdBook 0.4.52 built 37 pages with clean asset, link, and fragment validation.
- Isolated Kind E2E was not run in this workspace: Docker is absent, Podman is unreachable, and the active kubeconfig is the real `homelab-k3s` context. Per repository policy, no E2E command was redirected at that cluster. The authenticated-boundary Kind cases remain a CI/privileged-host gate.
- Graphify was incrementally rebuilt after the source and documentation changes; the tracked report and manifest are refreshed while large reproducible graph outputs remain ignored.

F01 still requires repository-owner GHCR package permission changes and a successful post-merge `main` publication. F07 remains separately owned dependency-alert work; no dependency upgrade is mixed into PR #13.

## Review coverage and evidence

Graphify scanned 230 supported files (112 code, 117 documentation, and one image), approximately 266,432 words. The rebuilt graph is pinned to baseline commit `29aaedf` and contains 1,961 nodes, 3,634 edges, 192 communities, and no detected import cycles.

The following passed against the baseline:

- `make test`
- `make lint` with zero issues
- `go test -race ./internal/...`
- mdBook 0.4.52 build
- local asset, link, and fragment validation across 36 generated HTML pages
- GitHub Tests, Lint, E2E Tests, Test Chart, Book build, and CodeQL workflows for `29aaedf`

The GitHub `Publish Images` workflow failed for the same commit. Details are in finding F01.

## Work already implemented on the remediation branch

### Graphify checkpoint

Commit `c93f160` tracks:

- `graphify-out/GRAPH_REPORT.md`
- `graphify-out/manifest.json`
- `graphify-out/cost.json`
- a narrow `.gitignore` rule that continues to exclude large Graphify outputs and caches

The Graphify cost file reports zero semantic tokens because the collaboration runtime did not expose subagent token counts. This is a measurement limitation, not a claim that semantic extraction used no tokens.

### Remote embedding response validation

The current branch adds an initial implementation for F03 in:

- `internal/embed/http.go`
- `internal/embed/http_test.go`

The implementation:

- makes `Dimensions()` concurrency-safe;
- treats response indexes as authoritative without relying on sorted array position;
- rejects missing, out-of-range, and duplicate indexes;
- rejects empty vectors;
- rejects mixed dimensions within one response;
- rejects dimension changes between responses;
- rejects NaN and infinity before normalization;
- adds tests for duplicate indexes, mixed dimensions, and cross-request dimension changes.

Before extending this work, run `gofmt` and the focused embedding tests. Add explicit tests for out-of-range indexes, empty vectors, non-finite values, and concurrent calls so every new guard has regression coverage.

## Findings

### F01 — P1 release blocker: published images cannot be pushed

Evidence:

- GitHub Actions run: `https://github.com/bryanbarton525/pulse/actions/runs/34412385522`
- Failure: `failed to push ghcr.io/bryanbarton525/pulse-controller:latest: denied: permission_denied: read_package`
- The workflow had `HAS_GHCR_TOKEN=false` and used `GITHUB_TOKEN` despite declaring `packages: write`.
- The failure occurred only after the multi-architecture controller build consumed approximately 23 minutes.

Impact:

- `main` is not currently releasable through the documented image workflow.
- Controller, probe-runner, and incident-engine images are not published by this run.
- Tag-based release assets depend on the publish job and will also be blocked.

Remediation:

1. In GHCR package settings, grant this repository Actions access to each existing Pulse package, or configure `GHCR_TOKEN` and optionally `GHCR_USERNAME` with package read/write access.
2. Add a fail-fast registry permission check immediately after login and before fetching models or building images.
3. Ensure the preflight distinguishes a package that does not yet exist from an existing package that is inaccessible, so first publication remains possible.
4. Keep `GITHUB_TOKEN` as the preferred path when repository/package linkage is correctly configured; a long-lived PAT should not be mandatory without need.
5. Re-run the workflow and verify all three multi-architecture manifests and their expected tags exist in GHCR.

Acceptance:

- The workflow succeeds on `main` and on a version tag.
- `docker buildx imagetools inspect` shows both `linux/amd64` and `linux/arm64` for controller, probe runner, and incident engine.
- Release asset creation runs after a version tag.
- An authorization failure is reported before expensive image builds.

### F02 — P1 security: incident read APIs are unauthenticated

Evidence:

- `internal/incident/server.go` authenticates `POST /observations` and `POST /results`.
- `GET /results`, `GET /incidents`, and `GET /topology` do not call `authorized`.
- `config/network-policy/allow-pulse-internal.yaml` grants namespaces labeled `metrics: enabled` access to port 9090. NetworkPolicy is port-level and cannot restrict those clients to `/metrics`.
- `StatusSyncer.fetchJSON` currently performs an unauthenticated `http.Client.Get`.

Impact:

- Any pod admitted through the metrics namespace rule can read monitored target information, current failures, inferred and declared topology, merge evidence, scores, and LLM investigation text.
- Network isolation does not compensate for the missing application-layer authorization because metrics and internal APIs share the same port.

Remediation options, in preferred order:

1. Require the reloadable internal bearer token on `/results`, `/incidents`, and `/topology`.
2. Teach the controller `StatusSyncer` to obtain the internal token from `pulse-probe-auth` and attach it to incident-engine requests. Preserve local tests by allowing an empty token only when the server itself is intentionally configured without one.
3. Separate `/metrics` onto a dedicated port and Service so Prometheus access does not imply incident API access.
4. Retain constant-time token comparison and token rotation behavior.
5. Add NetworkPolicy and HTTP authorization tests covering controller, runner, metrics collector, missing token, incorrect token, and rotated token.

Acceptance:

- All internal read/write endpoints return 401 for a missing or incorrect token when production authentication is configured.
- The controller continues to synchronize results, incidents, and proposals.
- Prometheus can scrape metrics without receiving access to incident data.
- Token rotation succeeds without restarting every component simultaneously.

### F03 — P1 reliability: malformed remote vectors can panic correlation

Evidence:

- `internal/embed/http.go` previously accepted vector width from the first response without validating later responses.
- `internal/embed/embed.go` deliberately panics when equal-space vectors have different lengths.
- `internal/incident/merge.go`, `internal/incident/novelty.go`, and `internal/anomaly/drift.go` call cosine comparison.
- The remote embeddings endpoint is user-configurable, so vector shape is external input rather than exclusively an internal wiring invariant.

Impact:

- A model change, proxy error, malformed server, or concurrent dimension discovery can terminate the incident engine instead of degrading to topology-only correlation.
- Invalid indexes can silently associate a vector with the wrong input.

Remediation:

1. Complete and validate the branch implementation described above.
2. Add tests for every rejected response shape and a concurrent test under `-race`.
3. Consider tagging remote embedding spaces with endpoint/model/dimension identity rather than the generic `minilm` label, so two distinct remote models cannot appear comparable merely because their widths match.
4. Keep `Cosine` strict for internal programmer errors, but ensure every external embedder validates its output before constructing a `Vector`.
5. Confirm the engine logs the embedding error and falls back to declared topology without crashing.

Acceptance:

- Malformed or changing remote responses return errors, never panics.
- `go test -race ./internal/embed ./internal/incident` passes.
- A failed remote embedder leaves deterministic probes and declared-edge correlation operational.

### F04 — P2 correctness: expired topology proposals remain in CR status

Evidence:

- `internal/controller/incident_status.go:248-250` returns when `len(proposals) == 0`.
- The status-clearing loop therefore never runs when the engine successfully reports an empty proposal set.

Impact:

- `AnomalyPolicy.status.inferredDependencies` can retain proposals after expiration, incident-engine restart, configuration removal, or inference reset.
- Operators may review obsolete dependency hypotheses as if they remain active evidence.

Remediation:

1. Return early only on a fetch error; an empty successful result is valid desired state.
2. Iterate policies with an empty `relevant` slice and clear stale status.
3. Log fetch errors instead of silently conflating an unavailable engine with an empty result.
4. Add a fake-client regression test starting with populated status and an engine response containing zero proposals.
5. Add tests for one policy losing all proposals while another retains relevant proposals.

Acceptance:

- Empty engine proposals clear every obsolete inferred dependency.
- Fetch failures retain previous status and produce a useful log message.
- Status writes occur only when content changes.

### F05 — P2 correctness: `lastSignalTime` can remain stale

Evidence:

- `buildIntelligenceViews` populates `CanaryIntelligenceStatus.LastSignalTime` from the incident's `UpdatedAt`.
- `intelligenceStatusEqual` compares incident ID, role, trigger, novelty, score, policy, and investigation, but omits `LastSignalTime`.

Impact:

- Repeated signals in the same incident do not update the timestamp unless another compared field changes.
- Operators and automation can interpret an active incident as stale even while new evidence is arriving.

Remediation:

1. Decide the API contract explicitly: latest signal time or latest material incident-state-change time.
2. If it means latest signal, compare metav1 times in `intelligenceStatusEqual` and add nil/equal/different tests.
3. If write volume makes that undesirable, rename and document the field rather than populating data that equality intentionally discards.
4. Consider a bounded update cadence if every probe interval would create excessive Kubernetes status writes.

Acceptance:

- The field's name, documentation, equality logic, and observed updates agree.
- Tests cover repeated signals in the same incident and steady-state write suppression.

### F06 — P3 maintainability: merged plan contains stale branch/PR state

Evidence:

- The beginning of `plan.md` says the implementation is on `codex/pulse-book` and calls PR #4 an active draft even though it is merged into `main`.
- Later sections describe completed content accurately, making the top-level state internally inconsistent.

Impact:

- A new agent can start from the wrong branch or mistake completed work for an open PR.

Remediation:

1. Replace the top checkpoint with the merged `main` commit and current remediation branch.
2. Move historical PR/branch details under a clearly labeled history section.
3. Keep production publication blockers explicit and separate from repository implementation status.
4. Link this findings document as the current remediation plan.

Acceptance:

- The first page of `plan.md` accurately identifies `main`, this branch, completed work, external blockers, and next action.

### F07 — Security dependency follow-up

Evidence:

- GitHub reported 10 dependency alerts on the default branch during the push: one critical, six high, and three moderate.
- This review did not retrieve the private alert details, so affected packages and exploitability are not yet established.

Remediation:

1. Review Dependabot alerts in GitHub and record package, affected component, reachable surface, fixed version, and compatibility risk.
2. Prioritize reachable runtime dependencies over build-only or development-only dependencies.
3. Merge existing safe Dependabot updates individually with full tests.
4. Do not claim the alert count alone proves ten exploitable Pulse vulnerabilities.

Acceptance:

- Every alert is upgraded, dismissed with a documented reason, or tracked with an owner and deadline.

## Recommended implementation sequence

### Checkpoint 1 — Preserve review evidence

Completed and pushed as `c93f160`.

### Checkpoint 2 — Harden remote embeddings

1. Finish F03 tests and formatting.
2. Run focused unit and race tests.
3. Run `make test` and `make lint`.
4. Commit and push independently.

### Checkpoint 3 — Correct status lifecycle

1. Fix F04 and add fake-client tests.
2. Resolve the F05 timestamp contract and add tests.
3. Re-fetch objects before status updates where practical to follow controller conflict-handling guidance.
4. Run controller unit/envtest tests, then full tests and lint.
5. Commit and push independently.

### Checkpoint 4 — Authenticate internal reads

1. Implement F02 with token rotation and local-development compatibility.
2. Separate metrics exposure if feasible in the same change; otherwise create an explicit follow-up with a restrictive interim policy.
3. Regenerate manifests only through `make manifests` if markers or generated configuration inputs change.
4. Run unit, race, envtest, chart, and isolated Kind E2E validation.
5. Commit and push independently.

### Checkpoint 5 — Restore publishing

1. Apply the external GHCR package/repository permission change.
2. Add the fail-fast workflow preflight.
3. Validate all three multi-platform images and tag release assets.
4. Commit the workflow improvement and link the successful run.

### Checkpoint 6 — Documentation and dependency closure

1. Update `plan.md` per F06.
2. Triage F07 with evidence from GitHub alerts.
3. Update operational/security documentation for internal API authentication and model degradation behavior.
4. Rebuild the 36-page book and run its link checker.

## Validation matrix for the completed branch

Run, at minimum:

```bash
gofmt -w internal/embed/http.go internal/embed/http_test.go
go test ./internal/embed ./internal/incident ./internal/controller
go test -race ./internal/embed ./internal/incident ./internal/controller
make test
make lint
npm ci --prefix book --ignore-scripts
npm run --prefix book assets
mdbook build
python3 book/check-links.py
```

Then validate in an isolated Kind cluster using the repository's supported environment instructions:

- deterministic HTTP and gRPC results still synchronize;
- authenticated runner-to-engine writes pass;
- authenticated controller reads pass;
- unauthenticated incident reads fail;
- token rotation recovers without data leakage;
- an invalid remote embedding response degrades correlation without restarting the engine;
- an empty proposal set clears policy status;
- `lastSignalTime` follows the chosen API contract;
- metrics remain scrapeable;
- the demo scenarios and their reset path remain deterministic.

## Constraints and cautions

- Keep Graphify HTML, raw JSON, and caches untracked unless the repository adopts Git LFS or changes its artifact policy deliberately.
- Do not bypass pre-commit hooks for generated or large files.
- Do not hand-edit generated Kubernetes artifacts.
- Use an isolated Kind cluster for E2E work and an explicit Kubernetes context.
- Treat the GHCR permission correction as an external configuration action; code changes alone cannot grant package access.
- Preserve deterministic monitoring when models or external actions are unavailable.
- Avoid logging bearer tokens, Secret values, response bodies containing credentials, or complete third-party error payloads.

## Definition of done

The remediation is complete when all P1 and P2 findings have regression tests, the full local and GitHub validation matrix passes, the three production images publish for both supported architectures, internal incident data is not exposed through metrics access, stale status is cleared correctly, malformed model output cannot crash the engine, documentation reflects the merged state, and every dependency alert has an evidence-backed disposition.
