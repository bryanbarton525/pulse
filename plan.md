# PR #13 Remediation Completion Plan

## Objective

Bring PR #13 into alignment with the repository remediation plan and close the remaining repository-owned work for findings F01-F06. Track F07 and the administrative portion of F01 as explicit post-merge work rather than making an otherwise mergeable PR depend on external configuration.

## Scope and decisions

- Deliver source, test, manifest, installer, demo, documentation, and review-evidence changes on the PR #13 head branch.
- Retarget PR #13 to `main` after the remaining fixes are pushed. Its current base, `codex/final-review-fixes`, has no delivery PR to `main`.
- Preserve the local uncommitted `plan.md` change on `codex/final-review-fixes` before switching branches; port it deliberately only if it is still needed after comparing it with the PR head.
- Treat GitHub package permissions, publication verification, and Dependabot-alert disposition as separately owned post-merge gates. Document their runbooks and evidence, but do not mix dependency upgrades into PR #13.
- Require a structurally valid Bearer authorization header for production operational endpoints. Tokenless access is permitted only through an explicit local-development mode; an absent or empty production token must fail closed.
- Preserve the selected network boundary: metrics and liveness on port 9090 (`http`), authenticated operational APIs on port 9091 (`api`).
- Preserve runner fallback for probe results. The incident-engine aggregator intentionally expires missing shards and must not become a hidden dependency of deterministic status projection.
- Regenerate only artifacts produced by the repository's documented commands; do not hand-edit generated Kubernetes resources.

## Implementation sequence

### 0. Establish the implementation and delivery branch

1. Save and inspect the existing uncommitted base-branch `plan.md` change without discarding it.
2. Fetch the latest remote state, switch to `cursor/cloud-agent-1789008034228-6x55c`, and fast-forward it to the PR head before editing.
3. Confirm the eventual `main...HEAD` diff contains the preserved Graphify checkpoint, initial embedding validation, and all PR #13 commits.
4. After implementation and validation, retarget PR #13 from `codex/final-review-fixes` to `main` and re-check the complete diff and CI.

### 1. Close the API authorization review findings

1. Replace permissive authorization-header prefix trimming in the incident-engine and probe-runner servers with a shared or equivalent strict parser that:
   - parses the authorization scheme instead of trimming an optional prefix;
   - accepts the standard Bearer scheme and exactly one nonempty credential;
   - rejects a missing scheme, raw token, another scheme, empty credential, and malformed or multi-part values;
   - retains constant-time token comparison after validating the scheme;
   - returns unauthorized when the production token is absent or empty.
2. Introduce an explicit unauthenticated local-development mode, defaulting off. Production workload manifests must not enable it. Keep an empty-token server closed unless that mode was deliberately selected.
3. Remove unused combined/legacy mux constructors, or route them through the same explicit auth policy, so future wiring cannot accidentally restore an unauthenticated `/results` endpoint.
4. Add table-driven server tests for missing, raw-token, wrong-scheme, empty-bearer, malformed, incorrect, correct, and dynamically rotated tokens. Cover production-empty-token rejection and explicit local-mode behavior on every protected read/write surface.
5. Replace duplicated literal Service port values in `ensureNamedService` with the existing controller port constants, preserving `http:9090` and `api:9091`.

### 2. Complete status synchronization correctness

1. Preserve the current incident-engine-to-runner fallback for `/results`. Add a regression test proving an empty or unavailable engine plus a healthy runner still updates deterministic canary status, while incident and proposal phases continue independently.
2. Verify and cover successful empty full-state responses:
   - empty incidents clear obsolete canary intelligence;
   - empty proposals clear obsolete `inferredDependencies`;
   - fetch/auth/decode failures retain the last successful projection and emit useful logs.
3. Extend policy reconciliation so an unreferenced policy, including after optional incident-engine removal, clears stale `inferredDependencies`. Define writer ownership explicitly:
   - the policy reconciler clears inferred dependencies only when reference count is zero;
   - the StatusSyncer projects proposals only for policies referenced by at least one probe.
4. Make status writes conflict-safe:
   - re-fetch the current object immediately before status mutation;
   - retry conflicts with `retry.RetryOnConflict` or the repository-equivalent controller-runtime pattern;
   - preserve fields projected by the other status writer.
5. Complete the `lastSignalTime` API contract:
   - document it as the latest signal for the active incident;
   - verify the existing one-minute timestamp-only update cadence;
   - update immediately for material incident changes and closure.
6. Add deterministic fake-client/unit coverage for result fallback; empty/error/partial incident and proposal responses; unreferenced policy clearing; writer ownership; conflict retries and cross-writer field preservation; and timestamp updates below/at/above the one-minute boundary.
7. Run and commit the supported generated outputs after the API documentation change:
   - `make manifests generate`
   - `make build-installer`

### 3. Repair the operator workflows before E2E

1. Update the demo helpers and scenarios to read `.data.internal-token` without printing it, establish and clean up authenticated port-forwards to port 9091, send bearer-authenticated operational API requests, and unset the token afterward. Do not use `kubectl get --raw`, because it cannot attach the required authorization header.
2. Update README, Quick Start, operations guide, book chapters, standalone documentation, diagrams, and Mermaid flows so:
   - operational reads use the authenticated 9091 API;
   - metrics examples remain unauthenticated on 9090;
   - token rotation, embedding-space identity, proposal expiry, and `lastSignalTime` semantics match runtime behavior.
3. Add focused tests for demo helper lifecycle and redaction behavior so tokens are not printed and port-forward processes are cleaned up on success or failure.

### 4. Finish authenticated-boundary integration coverage

1. Add controller and envtest coverage for Secret migration, `internal-token` refresh/rotation, authenticated controller-to-runner and controller/runner-to-engine requests, and sharded-runner request paths.
2. Extend isolated Kind E2E coverage to prove:
   - port 9091 rejects missing and incorrect credentials;
   - valid and rotated credentials recover without coordinated process restarts;
   - metrics remain scrapeable on 9090 and cannot access operational APIs;
   - empty incident and proposal responses clear the appropriate projected status;
   - the repaired demo inspection paths work against the deployed components.
3. Give rotation tests an explicit recovery bound that accounts for projected-Secret refresh, component reload, and the StatusSyncer interval. Permit transient 401 responses during the bounded convergence window, but require automatic recovery without coordinated restarts.
4. Retain existing embedding hardening behavior and run its focused/race coverage alongside incident and controller tests. Document an environment limitation only if it prevents the prescribed race test after attempting it.

### 5. Update findings and review evidence

1. Update `findings.md` with resolution commits, repository validation evidence, and clearly assigned post-merge F01/F07 actions.
2. Rebuild Graphify after final source and documentation changes. Commit only the small report, manifest, and cost artifacts already allowed by repository policy.
3. Resolve the current GitHub review threads with commit references, request a fresh review, and address any new blocking findings before merge.

### 6. Post-merge release and dependency closure

1. Repository owner: grant the Pulse repository Actions access to the existing GHCR packages (`pulse-controller`, `pulse-probe-runner`, `pulse-incident-engine`). Retain `GITHUB_TOKEN` as the standard credential.
2. After PR #13 is merged to `main`, run or observe the `main` publish workflow and record its URL. Do not create a release tag from an unmerged branch.
3. Use `docker buildx imagetools inspect` to verify `linux/amd64` and `linux/arm64` manifests and expected `main`/SHA tags for all three images.
4. Only after the successful `main` publication, create the next intended version tag and verify versioned manifests and release-asset creation.
5. Retrieve each open Dependabot alert and record advisory, package, affected component, runtime reachability, fixed version, compatibility risk, owner, and deadline.
6. Process dependency upgrades as separate, focused PRs. For every alert, merge a validated update, record a justified dismissal, or retain a tracked owner/deadline.

## PR #13 validation and merge gates

1. Run the focused Go tests for the changed embedder, server, controller/status, and policy code, including `-race` for the embedding, incident, and controller packages.
2. Run the existing repository test and lint targets, then manifest/installer generation checks.
3. Rebuild the book and run its existing asset/link validation commands after documentation edits.
4. Run the isolated Kind E2E suite using the repository-supported cluster setup and the new authenticated/API-boundary cases.
5. Confirm `main...HEAD` contains the intended complete remediation diff after retargeting.
6. Request a fresh GitHub code review after the authorization and port-constant comments are resolved. Confirm all review threads are resolved and no new blocking findings remain.
7. Keep commits unsquashed until review is complete, and do not modify unrelated worktree changes.

## PR #13 definition of done

- PR #13 has no unresolved security or correctness review comments.
- Findings F02, F04, and F05 have implementation and regression-test closure; F03 remains regression-safe.
- Demos and all operator-facing documentation use the correct 9090/9091 trust boundary.
- `findings.md` records repository resolution evidence and assigns the remaining GHCR and Dependabot actions.
- The complete remediation diff is deliverable to `main`, and all required repository CI checks pass.

## Post-merge closure criteria

- GHCR publishing succeeds from `main` for all three dual-architecture images before a release tag is created.
- The next intended version tag produces the expected versioned manifests and release assets.
- Every Dependabot alert has an evidence-backed upgrade, dismissal, or accountable owner and deadline.
