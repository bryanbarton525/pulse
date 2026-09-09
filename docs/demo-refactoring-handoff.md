# Pulse demo, runtime evidence, and documentation refactoring plan

Prepared 2026-09-05 for implementation by another GPT model.

## Assignment and working state

Turn the demo into a reliable engineering learning environment. An engineer should be able to explain the system, observe why a validation or intelligence signal fired, inspect the resulting actions, change a validation, and prove recovery. Implement the work below in reviewable stages; preserve existing product behavior unless an explicitly identified defect requires correction.

Repository: `/Users/bbarton/go/modules/pulse`.
Inspected branch: `feat/model-intelligence`.
Inspected HEAD: `4db4cbe887cd072bde8f8a97ad638fd15b98dc7a`.
This is the direct checkout, with substantial tracked modifications and untracked implementation files. HEAD alone does not contain the implementation reviewed here. Preserve all existing changes, inspect `git status` before starting, and do not reset, discard, commit, push, or merge unrelated work.

This handoff is a review and implementation plan, not an assertion that the proposed changes have been implemented. No application changes were made during this review. The review read the demo, relevant runtime paths, and documentation; it was not an exhaustive security or concurrency audit of every package.

Follow the repository AGENTS.md. In particular, regenerate CRDs, RBAC and DeepCopy files through `make manifests` and `make generate`; never edit generated artifacts by hand. Use Kubebuilder for new CRDs or webhooks. E2E runs must use an isolated Kind cluster. A demo refactor should not require a new CRD.

## Evidence and validation history

Earlier work ran the nine existing scenario paths against Kind, exercised the narrated status/content output, and reran novelty and outage after correcting their global action counters. That is historical evidence, not a current clean end-to-end pass: the entire final version was not rerun after every last change. One earlier full run failed because unrelated latency actions contaminated a global LLM counter; the replay incident itself correctly reported `novel=false`.

This review confirmed that local kubeconfig contains both `kind-pulse-demo` and `homelab-k3s`. Do not infer which context is active or use it implicitly. No current cluster health claim is made here. The existing browser page is `docs/quick-start.html`; browser selection began, but visual QA was not completed before the user requested this handoff.

## Confirmed findings

### F01 — Demo commands can operate on the wrong cluster (P1)

`Makefile` checks the Kind context in `demo-cluster`, but subsequent install, deploy, workload and inspection recipes use plain `$(KUBECTL)`. Scripts inherit the same implicit-context command. Selecting `DEMO_CLUSTER` therefore does not consistently select the Kubernetes destination. The prerequisites of `demo-up` are also independent and can run out of order under `make -j`.

Change: introduce one explicit demo context, defaulting to `kind-$(DEMO_CLUSTER)`, and consistently pass it through every recipe and script, including recursive `make deploy`. Sequence cluster creation, image loading, CRD install, operator deploy and workload readiness explicitly. Keep generic non-demo targets using their existing context semantics. Do not change the user's current context as a side effect.

Accept: a fake kubectl records the demo context on every demo call; a different current context cannot redirect actions; `make -j demo-up` preserves lifecycle ordering; a non-default demo cluster works.

### F02 — Recovery and convergence checks can give false success or false failure (P1)

`hack/demo/scenarios.sh:wait_for` turns a failed kubectl command into an empty string. Waiting for an empty incident ID can therefore succeed when the API is unavailable or the object is missing. `wait_correlated_outage` reads three IDs once after phases become Unhealthy, although incident status converges asynchronously. `wait_checks` counts an existing first timestamp and does not require observations after the mutation. Timeouts count sleeps rather than wall time and kubectl requests have no explicit deadline.

Change: separate read errors, missing results, stale results and valid observations. Use bounded subprocess/API timeouts and a monotonic wall-clock deadline. Poll a complete predicate until all members, roles and IDs converge. Capture mutation time and require fresh results after it. Treat API unavailability as a failed check with diagnostics, never healthy recovery.

Accept: simulated API failures, stale timestamps, missing objects and delayed member propagation cannot pass prematurely; delayed valid convergence passes; timeout messages include the last observation and actionable read error.

### F03 — Action identity and action completion are not robust (P1)

`llm_count_for` uses substring matching, so `inc-123-1` can match `inc-123-10`; an empty incident ID matches every LLM entry. `show-actions.py --incident` has the same substring problem. Counting an LLM request does not prove the response succeeded or later Slack/observability actions completed. Two quiet seconds of sink logs is only a heuristic. Novelty's initial occurrence can currently be non-novel and the test still continues, weakening the teaching claim.

Change: parse structured events with exact incident IDs, action types and timestamps. Require nonempty IDs. Assert all expected local sink receipts and the completed investigation where applicable, with an observation window for duplicate detection. Novelty must prove first occurrence novel with one investigation, recovery, then a distinct second incident with zero investigations and continuing notification/observability delivery. Keep unrelated incidents visible as background activity but exclude them from scenario counts.

Accept: prefix-colliding IDs, unrelated latency signals, malformed records, delayed Slack, failed LLM responses and duplicate calls are correctly distinguished. A novelty test cannot pass with zero investigations on both occurrences.

### F04 — The viewer does not show the evidence it promises (P1)

`hack/demo/05-sink.yaml` truncates request bodies at 4,000 characters without recording truncation. `show-actions.py` shows only the last LLM prompt, only 14 prompt lines, only the latest Slack text, and no observability body. Valid JSON with an unexpected shape can still crash it. The prior description of complete prompts/payloads is therefore inaccurate.

Change: use bounded structured sink records that preserve the full demo payload within a documented limit, record original byte length and truncation explicitly, and include receipt time and exact incident identity. Render every matching incident, all requested prompt messages and the observability JSON. Use collapsed/summary output where needed, with explicit full-output and machine-readable modes. Report credential presence/scheme without printing credential values. The local sink must always label its analysis as simulated.

Accept: a multi-member prompt exceeding 4 KB is either preserved or visibly reported as truncated; two incidents both appear; JSON arrays/scalars/null, invalid body strings and noisy logs do not crash the viewer; Datadog payload fields are inspectable; no tokens appear in ordinary output.

### F05 — Correlation narration is wrong, and merge evidence is discarded (P1)

`internal/incident/merge.go:Evaluate` merges on declared topology OR sufficient same-space similarity, subject to candidate/window filtering. A declared edge does not need a model. `scenarios.sh`, `lab.sh` and `demo_inspect.py` imply similarity requires topology or is constrained by it. `MergeDecision` contains evidence and similarity, but `engine.go:relatedFailures` keeps only `.Merge`. `Incident` therefore cannot explain the merge reason. The existing outage proves declared topology and real downstream propagation; it does not independently prove ONNX performed the merge.

Change: correct the narration immediately. Preserve bounded merge evidence on the internal incident read model: member pair, evidence type, measured similarity when actually evaluated, applicable threshold and relevant time. Do not present the synthetic `Similarity: 1` returned for a declared edge as a measured cosine. Explain root-cause selection separately as graph/onset ranking, not LLM diagnosis. Add a separate controlled similarity-only experiment with no declared edge and a dissimilar negative control, calibrated against the real embedded model.

Accept: topology-only merging works without embeddings; similarity-only merging works without declared edges; incompatible model spaces do not merge; time alone does not merge. The viewer shows the actual reason, not a reconstructed guess. Proposals remain distinct from declared active edges.

### F06 — Freshness and detector state are ambiguous (P1)

`demo_inspect.py` mixes live result scores with CR phase/status and omits live result timestamps. Unchanged CR status intentionally is not rewritten every probe. The engine can return null/empty results while restarting, which the viewer does not robustly handle. Zero or omitted drift scores cannot distinguish warming, disabled, unsampled and evaluated-zero. gRPC successful status can disappear due to the CR's omitted zero value. In `status_syncer.go`, missing results leave previous canary status untouched; the aggregate can expire a shard while a CR still looks Healthy.

Change: separate desired spec, latest live observation, persisted CR status and incident decision. Always show timestamp/age and classify missing/stale live results explicitly. Add a compact detector status representation only if necessary to explain warmup/sample/threshold states; keep it bounded and body-free. Define missing-result timeout semantics for CRs as a separate product decision: preferably transition to Unknown after a documented grace period with recovery when fresh data returns, without writing CR status every check.

Accept: a stopped runner never appears as freshly healthy in the demo; engine restart/null results render intelligibly; valid gRPC code zero is retained; zero score is never described as definitive evidence of normality during warmup. Product status changes require controller regression tests.

### F07 — The lab and reset contract are incomplete (P2)

`lab.sh:canary_for` selects catalogue for content, while the content scenario changes unrelated. `demo-restore` restores deployment behavior, not edited canary specs or policy tuning. Reapplying the YAML is not a guaranteed reset for fields introduced by arbitrary patches outside its last-applied field set. Every chapter restarts the engine, clearing novelty, history and learned proposals, but the guide does not explain this. Baseline warming always observes five catalogue updates although latency has eight warmup checks and two dependent canaries.

Change: map each experiment to its actual subject. Define separate behavior recovery and explicit definition/reset operations. Snapshot only the demo-owned fields an experiment changes, restore those on request, and preserve unrelated user fields. Explain engine state loss and make chapter isolation explicit. Inspect effective warmup settings and fresh observations for all relevant probes; do not reset a runner while targets are broken. Add an optional paced tour that stops after evidence, plus a noninteractive mode for CI.

Accept: each lab inspection matches the launched experiment; customized assertions and overrides have a tested restoration path; rerunning a chapter is deterministic; novelty retains state only across its deliberate two occurrences; guided pauses do not hang noninteractive execution.

### F08 — Several examples overclaim their scope (P2)

`cmd/demotarget/main.go` returns a login page and an always-authenticated healthy session without issuing or validating cookies. It demonstrates ordered assertions, not a working session flow, despite commentary in `20-canaries.yaml`. `green-deploy` changes an environment variable on one Deployment; it is not blue/green traffic switching. The shop has six deployments, not five. Policy comments and the guide say only warmups differ from defaults, but novelty threshold, settling, cooldown and other settings also differ. Novelty skips the LLM, not all notifications.

Change: either implement a real cookie round trip with rejection when the cookie is absent, or accurately label the current journey. Prefer implementing the cookie behavior with a focused target test. Label the existing deployment scenario as a rollout with semantic regression. Add a true two-revision/service-selector blue-green lab only as a separately scoped extension; do not rename the current behavior to imply it. Document every demo-specific policy override and its teaching purpose.

Accept: session validation fails without the demo cookie and succeeds with the shared jar if implemented; prose matches the actual rollout mechanism; simulated LLM results cannot be mistaken for ONNX or remote generative inference.

### F09 — Documentation has diverged from the runtime (P1/P2)

`docs/operations.md`, `testing-and-validation.md`, `architecture.md`, `architecture-summary.md`, `reconciliation-design.md` and `scaling.md` still describe or use `Deployment/pulse-probe-runner`. Current reconciliation creates a StatefulSet, with optional incident-engine Deployment and sharding. The quick-start includes obsolete sample tables and no failure/recovery troubleshooting path. `model-intelligence.html` claims a startup warmup burst; no corresponding burst was found in the inspected runner. Its observability prose promises investigation content, while `internal/actions/observability.go:record` does not contain an investigation field.

Change: reconcile all these docs against source. Verify the burst claim across runner scheduling before removing it. For observability, either accurately document the existing record or explicitly add and test bounded investigation content as a product change. Label historical design proposals, including unimplemented canary types, as proposals. Make README link directly to the runnable Kind walkthrough and prerequisites; retain the local-controller path with its DNS/results-URL limitations.

Accept: every documented resource command addresses the current kind/name; documentation claims map to an implementation or are labeled limitations/proposals; local links and anchor navigation work; sample output is explicitly illustrative and matches output fields.

## Recommended implementation structure

Keep Make as a thin public command surface. Consolidate demo orchestration, subprocess execution, polling, scenario definitions and evidence collection into a small Python standard-library package under `hack/demo/`. Python is already a prerequisite. Retain the existing Make target names as compatibility entry points; remove duplicated shell orchestration only after equivalent behavior is tested.

Suggested boundaries (names can change):

- `cluster.py`: explicit context, bounded kubectl calls, JSON decoding and useful errors.
- `scenarios.py`: a registry of scenario metadata plus explicit functions for complex outage/novelty sequencing. Avoid a generic workflow language.
- `evidence.py`: immutable timestamped snapshots and exact incident/action matching.
- `render.py`: terminal summaries/full detail and machine-readable reports, separate from collection.
- `cli.py`: explain, inspect, run, restore, validate and paced-tour entry points.
- `tests/`: focused fake-cluster regressions and fixture-based renderer tests.

Each scenario definition should state: learning objective; target CR/policy fields; setup/reset scope; mutation; required fresh observations; expected health and intelligence state; incident-member/role predicates; expected action receipts; recovery predicate; and a follow-up experiment. Preserve complex logic as code when a data registry would obscure it.

Persist an optional run report containing run ID, context, source revision/dirty indication, image IDs, model identifiers, effective policy, timestamps, observations, incident evidence and action summaries. This lets engineers compare runs and lets CI retain failure evidence. Never capture the generated auth Secret or raw production response bodies. A live browser dashboard is not required for the first refactor; a static report rendered from the same evidence is a useful later extension.

## Runtime investigations and targeted fixes

These items must not be silently mixed into a presentation-only change.

1. **Zero-variance latency baseline: confirmed code gap.** `internal/anomaly/latency.go` leaves z-score at zero when variance is zero, then absorbs a slower sample. A perfectly constant baseline can miss the onset of a real slowdown. Existing tests deliberately use jitter. Add a constant-baseline regression and choose a documented minimum deviation or absolute-change rule; measure false positives on low-latency jitter before choosing defaults. Do not use infinity in JSON.
2. **Global engine lock during embedding: confirmed design concern.** `classifyNoveltyLocked` invokes embedding using `context.Background()` while holding the engine mutex. With a slow embedding backend this can delay ingestion and read endpoints. Measure with a blocking fake; move expensive work outside the lock using an incident generation/version check, with explicit cancellation and timeout. Do not write stale classification into a recovered or changed incident.
3. **History retention: confirmed unbounded key growth risk.** `Aggregator.history` limits entries per probe but has no inspected deletion path for removed probe names. Add expiry/retention aligned with active shard ownership, with tests for deletion, name churn and resharding. Preserve necessary short incident history while bounding total memory.
4. **Dispatch lifecycle and concurrency: investigation required.** Test late members, changed roots/policies, recovery during an LLM request, engine shutdown, and concurrent reload. Do not assume the debounce snapshot or investigation write-back is valid for a later incident generation. Validate delivery semantics and retry/idempotency expectations before changing them.
5. **Model failure and state lifetime: investigation required.** Verify unavailable embeddings, conflicting policy model choices, model reload/space changes, sharding/reassertion and novelty settling. Prove deterministic monitoring continues and failure to obtain an embedding does not silently masquerade as a measured normal score. Keep ONNX in the incident engine and Potion in the runner unless an independently justified design change is reviewed.

Do not introduce a database, durable incident CRD, new authentication scheme, new model architecture or broad controller rewrite as a prerequisite for this demo. Document current in-memory state limits and treat durability/HA as a separate project.

## Delivery sequence

1. **Baseline and execution safety:** record dirty tree; inspect current commands; implement explicit context and ordered startup; add fake-kubectl regressions for F01/F02.
2. **Evidence and identity:** structured sink records, exact incident parsing, full renderer, robust completion predicates, freshness handling (F03/F04/F06). Add captured-fixture tests.
3. **Scenario refactor:** common client, registry, real warmup predicates, behavior/definition restoration, explicit isolation and paced mode (F07). Preserve public target names.
4. **Explainability and fidelity:** expose bounded merge evidence, similarity-only test, correct correlation narrative, real cookie journey, honest rollout naming (F05/F08).
5. **Runtime corrections:** address zero-variance latency and bounded history in separate commits; investigate lock/dispatch/model-lifecycle concerns with regressions before changing behavior.
6. **Documentation reconciliation:** update README and every affected architecture/operations/testing guide, then the HTML walkthrough and model guide. Keep one canonical scenario matrix; check duplicated values automatically where practical.
7. **Acceptance run:** cold setup on an isolated named cluster, full tour, individual reruns, manual validation changes/restoration, deliberate API/target failures, final recovery, and desktop/narrow-screen visual QA. Capture failures as evidence, never hide them with looser counts or higher thresholds.

## Acceptance matrix

| Scenario | Required evidence |
| --- | --- |
| HTTP status | Desired 204, observed 200, failed probe, its incident and actions, then fresh recovery |
| Content | Desired marker on unrelated, HTTP still 200, missing-marker message, separate from drift |
| Journey | Step one passes, cookie/session flow if implemented, named second-step failure and recovery |
| MCP | Successful protocol exchange, missing required tool, capability/tool-contract diagnosis |
| gRPC | Transport success with NOT_SERVING distinguished from connection failure and SERVING |
| Body drift | Passing HTTP assertion, warmed Potion score above threshold for required breaches, body-free shipped signal |
| Latency | Warmed baseline, passing delayed calls, explicit z-score/threshold, expected separate latency incidents |
| Declared correlation | Catalogue root and two real callers in one incident; independent control separate; declared-edge evidence |
| Similarity-only correlation | No declared edge; actual ONNX similarity and threshold justify merge; dissimilar control separate |
| Novelty | First new incident investigated; recovery; second incident known; zero second LLM calls, continued other actions |
| Restore | Fresh healthy results for all demo probes and no open demo incidents; edited fields restored when explicitly requested |

For code verification run targeted package tests first, then the repository-required `make lint-fix` and `make test` after Go edits, inspecting generated/formatting changes. Use race tests for touched concurrent packages. For API/marker changes run manifests/generate and regenerate release bundles/charts through their supported flow.

Real-model verification must explicitly establish model files and the native ONNX library are present. Inspect `internal/embed/onnx_real_test.go` and the runtime env constant before invoking tagged tests; a skipped real-model test is not a pass. Use the real-model container if host architecture/library setup is unsuitable. Keep fast fake-model tests as a separate layer.

For demo tests, use fake kubectl/fixtures to exercise errors without a cluster, then the isolated Kind cluster for actual models/protocols. Include a second chapter run, a full noninteractive run, and a failed mid-run action with diagnostic report. Do not claim the full final tour passed based on earlier versions or only individual chapters.

## Documentation and learning experience requirements

The quick-start should provide a short navigation list and a clear sequence: prerequisites/setup; architecture; real versus simulated components; validation catalog; paced tour; evidence interpretation; hands-on labs; recovery/troubleshooting; teardown.

Explain these distinctions explicitly:

- Protocol pass/fail versus intelligence signals on passing probes.
- Potion embeddings versus EWMA latency versus MiniLM similarity versus simulated LLM analysis.
- Declared-edge correlation versus similarity-only correlation; root ranking versus generated investigation.
- Live probe timestamps versus persisted CR timestamps; no data versus zero score.
- Novelty gating LLM calls versus cooldown/rate limits gating actions.
- Engine resets losing novelty/proposals/history versus runner resets losing baselines.
- Backend payload delivery being real HTTP to a local sink; the narrative response being a deterministic fixture.

Include a table for all scenario fields and expected outcomes, copyable context-pinned commands, expected wait behavior, and two explicit lab cycles: change a status/content assertion and change a model threshold. Each must include an observation and a verified undo. Explain that changing policy/overrides may reload runtime configuration and affect detector state; verify exact reset behavior in code.

Troubleshooting must cover missing context, unavailable API, image pull/build problems, unresolved models, warming/no results, customized contracts preventing restore, throttle/novelty suppression, sink truncation, and interrupted runs. Do not represent errors as empty success output.

Visual QA must check navigation anchors, long tables/code overflow, readable mobile widths, disclosure controls, and local links. Do not turn the guide into a mock dashboard with invented live values.

## Required final handoff from the implementing model

Provide the changed files and rationale, confirmed findings fixed, tests actually run (including skips/failures), the exact context and final state if a cluster was used, commands to run the improved tour/lab, remaining limitations, and any separately deferred product decisions. Keep changes reviewable and do not publish or push unless requested.
