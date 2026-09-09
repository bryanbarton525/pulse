# Pulse Book implementation plan

Status: repository implementation is complete on the current `codex/pulse-book` branch head; external publication is blocked. Planning PR #3 merged into `feat/model-intelligence`. The documentation site has not been deployed.

## Current handoff checkpoint

Active draft PR: [#4 — implement Pulse Book and manual learning course](https://github.com/bryanbarton525/pulse/pull/4). First implementation commit: `2ad32a3`.

### Cursor handoff — latest checkpoint

Review pass after the completed course: fixed chapter order, stale contributor steps, and a few runtime-claim errors. The SUMMARY now teaches policy, Potion drift, and latency on `book-shop/catalogue` before the `shop` fixture install, so the policy chapter's empty-engine starting state and cleanup remain true. Expected ConfigMap evidence is the probe `book-shop/catalogue` with `policy: book-shop/book-triage`. Novelty now names the five-second dispatch debounce separately from the settling period. The gRPC chapter installs pinned `grpc-health-probe@v0.4.56` and points at the matrix Go client. The intelligence install pre-loads `python:3.12-alpine` and explains the `book-shop` versus `shop` transition. The contributor exercise inspects the already-merged HTTP interval maximum and applies the same bound to `GrpcCanary`. `README.md` now states Go 1.26.1+.

The complete manual course is now present and connected: deterministic HTTP, 204, journey, MCP and gRPC chapters; intelligence installation, policy, Potion drift, latency, topology/similarity incidents, novelty, actions, degraded models/results, sharding, recovery, a contributor exercise, and an experiment matrix. Existing architecture, reconciliation, scaling, operations, Helm, testing, development, CRD, and journey sources are included as canonical references. `README.md` links all three entry paths, and `DEBUG_GUIDE.md` is a preserved redirect instead of a diverging copy.

The generated book has 36 pages. mdBook 0.4.52 build and all local links/fragments pass. Browser verification passed sidebar navigation, nested `/pulse/` routes (formerly `/book/`), `failureNovelty` search, previous/next keyboard navigation, diagrams on two pages in Light and Navy themes, and the 400px incidents layout. No Mermaid render error was set; only the known benign highlight.js Mermaid warning appeared.

Model preparation remains reproducible: pinned Potion/MiniLM revisions and four source hashes, 512-dimensional/63,091-row Potion conversion, real Potion tests, and tagged MiniLM ONNX tests with ONNX Runtime 1.22.0. All four Podman images now build with fully qualified base images. Container smoke validation loaded the 512-dimensional Potion model in the runner and MiniLM in the native incident engine. A passing 200 response produced measured Potion distance `0.15828632906491535`, an observed `bodyDrift` incident, and exactly one successful metric, LLM-fixture, Slack, and observability action. The fixture records showed the deterministic investigation, Slack payload, and Datadog payload; no real credentials were used.

Protocol smoke validation against the built images observed a healthy two-step cookie journey, MCP initialize/202 initialized/tools-list with two tools, and named gRPC `SERVING`. Mutations produced the exact runner evidence for journey step-two content failure, missing `health.check`, and `NOT_SERVING`; stopping the gRPC server produced code `Unavailable`. HTTP target and runner results were also inspected directly.

The contributor exercise is implemented rather than illustrative: `HttpCanary.spec.interval` now has a generated maximum of 3600, with an envtest admission regression rejecting 3601. The focused regression and the complete non-E2E test suite passed with Kubernetes 1.35 envtest assets. `make test` itself reached passing package results but this machine's downloaded Go toolchain lacks `covdata`, so its coverage-file command exits nonzero; CI is the authoritative coverage invocation. `make lint-fix` is similarly blocked locally because the plugin-built linter reports Go 1.24 while the module targets 1.26.1.

Earlier clean `kind-pulse-book` evidence remains valid for Podman/Kind on arm64: controller/runner/target install, CRDs, first canary healthy/failure/recovery. This cloud machine cannot execute the new clean-cluster prose order: nested Kind reaches kubeadm but cannot bootstrap because the parent container lacks required host capabilities. Docker is not installed, so Docker portability remains untested here.

Publication target is now the shared docs hub at `https://docs.iambarton.com/pulse/` (not `pulse.iambarton.com/book/`). Homelab application path is `apps/docs_site/` with Argo Application `docs-site`. Live HTTPS verification still requires a control-plane kubeconfig on omarchy (this host is a k3s agent with Kind contexts only).

Branch: `cursor/docs-hub-pulse-319b` (docs hub path retarget). Cursor bootstrap is `npm ci --prefix book --ignore-scripts`, `npm run --prefix book assets`, mdBook 0.4.52, and `python3 book/check-links.py`. Serve `book/build` beneath `/pulse/`. Use explicit Kubernetes contexts and do not represent unavailable Kind/Docker/production gates as passed.

### Completed foundation and validation

Added Mermaid 11.17.2 with npm lockfile and locally generated assets, component and sequence diagrams, CRD/journey reference includes, and a generated-HTML link/fragment checker wired into CI. Updated stale development prerequisites and scaffolding guidance. Dependency installation reported zero vulnerabilities at that checkpoint.

Pre-commit recovery: generated `graphify-out/graph.json` and `graph.html` were accidentally staged and exceeded the large-file hook limit. Exclude local graph output and `cmd/homelab.code-workspace` from commits; preserve both on disk. Generated Mermaid assets remain ignored and are reconstructed from the lockfile during builds.

All four pre-commit hooks passed after removing local generated files from staging. The manual environment run verified node Ready, no preinstalled CRDs, and the bounded CoreDNS rollout check. `pulse-book` now contains the deterministic installation and recovered first canary; `pulse-demo` is a separate existing cluster. Kind changed the current context, so continue using explicit contexts.

Completed in the first implementation increment:

- Added `book.toml` with `/pulse/` site URL (retargeted from `/book/`), chapter navigation, search through mdBook defaults, and light/dark themes; pinned mdBook 0.4.52.
- Added a substantive component/ownership overview and manual isolated Kind environment chapter. Neither uses Make or demo orchestration.
- Added introduction, book contributor instructions, and includes of canonical operations/development pages; unfinished course chapters are not listed as completed content.
- Added a CI book build and downloadable preview artifact. This workflow does not publish the production site.
- Ignored generated `book/build/` output.

Build tooling: official mdBook 0.4.52 for macOS arm64. Existing runtime test suites were not repeated for these documentation-only increments. Manual lab evidence is recorded above; it is not full course or model validation.

Next agent: start with `git status`, this checkpoint, and the implementation PR. Preserve unrelated untracked `cmd/homelab.code-workspace` and `graphify-out/`.

Remaining work requires an environment with the missing external capabilities:

1. Re-run the full prose order on a fresh privileged Kind host, including the new engine, action, sharding, restart, stale-result, and exact cleanup commands. Save per-chapter evidence rather than substituting the maintainer harness.
2. Execute the separate Docker path. Current evidence covers Podman image builds and local container networking only.
3. Finish homelab `docs_site` GitOps: seal `ghcr-pull-secret` for namespace `docs-site`, merge control-plane kubeconfig on omarchy, sync Argo, and verify `https://docs.iambarton.com/pulse/`.
4. Resolve any authoritative CI failures and retain the reproducible archive checksum produced by the book workflow.

Known gaps are therefore deployment/access gates, not missing book chapters: privileged clean-cluster execution where still needed, and live docs-hub publication (kubeconfig + sealed pull secret). Do not report the production site complete based on the successful local build or container-level runtime evidence.

Target: `https://docs.iambarton.com/pulse/`.

Hosting repository: [bryanbarton525/homelab](https://github.com/bryanbarton525/homelab/tree/main/apps). The shared docs hub belongs at `homelab/apps/docs_site/`; deployment resources follow that repository's separate `clusters/` convention. Documentation stays alongside Pulse code, and the site consumes a pinned Pulse documentation revision to avoid divergent copies.

Baseline: `e786e52` on `feat/model-intelligence`, containing the validated demo and E2E fixes. This planning PR is stacked on that branch; retarget it to `main` after the parent PR merges, preserving a documentation-only diff.

## Outcome

Create a complete learning path that lets an engineer install Pulse manually, explain each component and model, author canaries and policies, inject and diagnose failures, recover the environment, and make a tested contribution. Preserve the quick start for fast demonstrations and the operations guide for ongoing administration. Publish all three as connected paths in a searchable book.

The deep dive must not depend on Make targets, `demo-up`, `demo-validate`, or the scenario driver to complete a chapter. Explain and execute the underlying commands individually. Using a compiler, container builder, model-conversion utility, or manifest renderer is appropriate when its inputs, outputs, and purpose are explained; a one-command deployment wrapper is not the learning path.

## 1. Establish documentation and runtime truth

- Inventory README, DEBUG_GUIDE, all `docs/` pages, examples, Make targets, demo manifests, workflows, and model build recipes. Record the canonical destination and migration treatment of each document.
- Trace actual configuration from CRDs through reconciliation, runner configuration, observations, incident aggregation, actions, and status projection. Check prose and diagrams against code, not earlier screenshots or illustrative output.
- Correct known review targets: default/no-model versus ONNX-tagged test coverage; unchanged healthy CR timestamps versus live result freshness; model runtime/API version comments; and the scope of the existing two manager E2E specs.
- Investigate deployment constraints surfaced during validation, including runner pod security under a restricted namespace. Do not describe a manager-only E2E pass as proof that all runtime components work under that policy.
- Review all copied shell commands for explicit cluster context, bounded waits, cleanup, prerequisites, and secret handling. Scope lab resources to an isolated `pulse-book` cluster.
- Maintain a provenance table for commands and diagrams: source code/configuration, expected behavior, and the test that verifies the claim.

Deliverable: documentation inventory and a corrected architecture contract, with unresolved product limitations clearly identified.

## 2. Build the book foundation

Use mdBook as the proposed static documentation generator. Its chapter structure, search, syntax highlighting, theme support, and navigation match the requested book experience. Confirm compatible pinned versions of mdBook, Mermaid integration, and the renderer before committing the toolchain.

Proposed files:

```text
book.toml
book/src/SUMMARY.md
book/src/introduction.md
book/src/quick-start/
book/src/learn/
book/src/architecture/
book/src/operations/
book/src/reference/
book/src/contributing/
book/theme/
book/examples/
.github/workflows/docs.yml
```

The layout above is in the Pulse repository and owns book content, rendering, and validation. The site application and deployment live in homelab:

```text
apps/docs_site/                     # Dockerfile, nginx, hub index, /pulse/ book, /prism/ stub, runbook
.github/workflows/docs-site-build.yml
clusters/namespace-docs-site/       # Kustomize deployment, Service, HTTPRoute on shared gateway
clusters/argocd/docs-site.yaml      # Argo CD Application
```

These follow the existing `apps/iambarton_site`, image-build workflow, namespace manifests, and Argo CD Application pattern. Keep application source under `apps/` and cluster configuration under `clusters/`.

- Use a collapsible chapter sidebar, previous/next navigation, local search, copyable code blocks, deep links, light/dark themes, and readable mobile layouts.
- Preserve Pulse branding while adopting the restrained reading layout of the Kubebuilder Book. Do not copy its logos or project-specific content.
- Bundle Mermaid rendering assets with the site; diagrams must remain readable in both themes and have a prose equivalent.
- Configure and test all navigation, images, fonts, search assets, and diagrams under `/pulse/`, including direct navigation to nested chapters.
- Provide a plain-command local preview workflow and a reproducible static build. Avoid requiring Kubernetes just to edit prose.
- Show source/edit links, the documented release or commit, and a clear distinction between stable release documentation and development changes.

## 3. Write the manual learning path

Every chapter must include: learning objective; prerequisites and starting state; exact commands and editable YAML; why each step exists; expected output with variable values identified; inspection commands; failure symptoms; bounded recovery/reset; and a checkpoint question or small exercise. Label captured output with its tested revision; label all illustrative output explicitly.

### Part I — Environment and installation

1. Explain Kubernetes desired state, CRDs, controllers, reconciliation, Services, namespaces, RBAC, ConfigMaps, Secrets, and the difference between configuration and observed status.
2. Check host tools and resources; provide separately tested Docker and Podman instructions. Create a dedicated Kind cluster by writing its configuration and invoking Kind directly. Verify node readiness and the chosen context.
3. Clone/select a documented Pulse revision. Introduce the repository layout, generated-file rules, and how an engineer traces an API field into runtime behavior.
4. Obtain Potion and MiniLM artifacts step by step. Explain their upstream sources, licenses, download/conversion commands, integrity checks, vocabulary formats, dimensions, disk footprint, and where each artifact is used. Explain why conversion is required rather than hiding it behind `fetch-models`.
5. Build each image separately and load it into Kind. Explain controller, runner, incident engine, and demo-target images; ONNX build tags, cgo/native library compatibility, architecture differences, and image pull policy.
6. Inspect and install generated CRDs. Explain schema validation, defaults, status subresources, and why generated CRDs are regenerated from markers instead of hand-edited.
7. Author/inspect namespace, service account, role/bindings, configuration, network policy, and manager Deployment. Render only the required manifests, inspect the resulting YAML, and apply resources in explicit dependency order.
8. Verify manager readiness, permissions, metrics access, and logs. Explain what exists before the first canary and what reconciliation creates afterward.

### Part II — Deterministic monitoring

9. Deploy a small HTTP target by writing its Deployment and Service. Inspect selectors, endpoints, DNS, ports, readiness, and an actual response.
10. Write the first `HttpCanary`. Observe the generated runner configuration, StatefulSet, service discovery, first live result, and persisted status. Explain why the learner must not manually maintain the operator-owned runner alongside the controller.
11. Extend the HTTP contract: expected status, body marker, headers/authentication, timeout, TLS behavior, and supported response limits. Make an intentional assertion error and diagnose it from evidence.
12. Build the 204-response example, then the two-step login journey. Verify cookie/session behavior and identify exactly which step failed.
13. Deploy and inspect the MCP endpoint. Perform the protocol exchange manually, inspect capabilities and tools, then configure the MCP canary and remove a required tool.
14. Deploy the gRPC health service. Query its named service with a documented client, configure `GrpcCanary`, and distinguish transport failure from `NOT_SERVING`.

### Part III — Intelligence from first principles

15. Write `AnomalyPolicy` and enable it on one canary. Explain policy references, namespace behavior, defaults, overrides, and the optional incident-engine lifecycle. Inspect effective runner/engine configuration.
16. Explain Potion: normalization/tokenization, static token-vector lookup, mean pooling, normalization, 512-dimensional space, cosine distance, baseline learning, sampling, thresholds, and debounce. Change a passing response semantically and inspect the actual drift score and sample count.
17. Explain latency independently of embeddings: EWMA statistics, warmup, variance, z-score, and per-canary baselines. Introduce delay and explain why downstream callers can raise separate latency signals.
18. Explain MiniLM and ONNX: tokenizer, transformer inference, pooling, 384-dimensional space, failure normalization/redaction, and why vectors from different models cannot be compared. Establish loaded native runtime and model evidence; do not count a fallback or skipped test as model validation.
19. Configure declared dependency edges and inject an upstream outage. Follow propagated failures into one incident, retaining a distinct negative control. Explain that a declared edge can merge failures without a model.
20. Configure two independent canaries with no dependency edge. Inject identical failure text and a dissimilar control; inspect measured similarity, configured threshold, merge evidence, and separate incident IDs. Explain that similarity and root ranking are heuristics, not proof of causation.
21. Explore root selection, onset timing, incident membership, temporal windows, topology proposals, and action scheduling. Separate inferred proposals from active declared topology.
22. Recover and replay a failure. Compare incident IDs, novelty classification and clustering threshold, and action counts. Explain state lifetime, settling periods, cooldowns, and rate limits, including when novelty is not evaluated.

### Part IV — Actions, recovery, and contribution

23. Deploy the local recording sink manually and configure action credentials and endpoints. Trace the actual prompt, simulated response, status investigation, Slack payload, observability record, and metrics. Clearly distinguish real local embeddings from the deterministic LLM fixture and external generative inference.
24. Teach model-unavailable behavior and stale/missing results with controlled experiments. Explain which deterministic checks continue, what fallback grouping can do, and which state is lost on runner or engine restart.
25. Configure multiple runner shards and verify probe ownership, aggregation, and cross-shard correlation. Inspect resource use and document tested scale without making unsupported capacity claims.
26. Recover each target and undo each custom definition explicitly. Verify fresh passing results and closed incidents. Delete only the named lab cluster and explain which local artifacts remain.
27. Complete a contributor exercise: change a supported validation with a meaningful regression test, regenerate any affected artifacts through their generators, run appropriate tests, build/load the changed image, and replay the manual scenario. Link to API scaffolding instructions using the required Kubebuilder CLI.

## 4. Make failures inspectable and reproducible

Provide a chapter-linked experiment matrix covering status mismatch, missing body marker, journey/session failure, missing MCP tool, gRPC unhealthy service, passing semantic drift, latency shift, dependency outage, similarity-only grouping, novelty replay, missing model, and stale results.

For each experiment specify:

- Baseline and minimum samples/readiness required before mutation.
- The exact manual change (application control request, YAML edit, or resource update), including how to inspect its effect independently of Pulse.
- Expected protocol result, detector signal, incident membership/merge evidence, novelty state, and action counts.
- What the model contributes and what deterministic logic contributes.
- A negative control and a counterexample where useful.
- Bounded waits and an explicit restore procedure; no background scenario driver required.

Explain the demo control endpoint as a test fixture, including its scope and access assumptions. In-place behavior mutation simulates a release; it is not a Kubernetes blue/green rollout. If teaching real blue/green deployment, add a distinct chapter with two Deployments and a Service selector change, measure any transitional errors, and distinguish those errors from semantic drift.

## 5. Refine existing guides and architecture diagrams

- Consolidate canonical content instead of maintaining diverging copies. Keep existing README and HTML entry points working with links or tested redirects.
- Quick start: concise setup, automated tour, output interpretation, recovery, and pointers to the manual chapter explaining each command.
- Operations: deployment configuration, authentication, network policy, metrics, freshness, model health, state loss, sharding, troubleshooting, upgrades, and known limitations.
- Reference: CRD fields/defaults/validation, policy/model settings, runtime flags/environment, endpoints, and action semantics, verified against current source.
- Architecture: add Mermaid component/ownership diagrams; reconciliation sequence; hot-path probe/drift sequence; cold-path observation/merge/novelty/action sequence; declared versus inferred topology; sharded aggregation; and restart/state-lifetime diagrams.
- Label arrows by payload and direction, including trust boundaries and data retention. Distinguish retained local bodies, normalized failure text, embeddings, observations, configuration, and status. Verify claims about what crosses a process boundary against implementation.
- Link the learning path to contributor guidance, code locations, tests, and the operations troubleshooting sections.

## 6. Publish at docs.iambarton.com/pulse/

1. Implement the shared docs hub at `homelab/apps/docs_site/`. Follow the inspected homelab pattern: GitHub Actions builds application images into GHCR, Argo CD reconciles a Kustomize directory under `clusters/`, and Gateway API HTTPRoutes expose the site on the existing `iambarton-site-gateway`. GitHub Pages is not the deployment target.
2. Keep canonical book sources in Pulse. Pin a Pulse commit (`PULSE_REF`) in the homelab site build; record it in image labels and `/pulse/VERSION.txt`. Define an explicit promotion PR that updates this pin and the deployed image digest together. Do not fetch a moving branch at container startup.
3. Serve the generated static content beneath `/pulse/` in the site container. Test `/pulse` redirection and nested routes without stripping the prefix incorrectly. Serve a minimal hub index at `/` and a stub at `/prism/` for future books.
4. Add `homelab/.github/workflows/docs-site-build.yml` for GHCR publication. Pulse's docs workflow validates/builds the book; homelab owns site packaging and deployment.
5. Add the namespace, Deployment, Service, readiness/liveness probes, resource settings, and Kustomize configuration under `clusters/namespace-docs-site/`, plus `clusters/argocd/docs-site.yaml`. App-of-apps picks up Applications from `clusters/argocd/`.
6. Attach HTTPRoute host `docs.iambarton.com` to the shared gateway with ExternalDNS hostname annotation. Do not add the hostname to cloudflare-ddns `records:`. Reuse the `*.iambarton.com` certificate on the existing gateway.
7. Open linked Pulse and homelab PRs, documenting source revision, image digest, routing, and rollout order. Verify HTTPS, `/pulse` to `/pulse/` handling, nested chapter URLs, search, diagrams, and cache behavior. Roll back by reverting the pinned source/image deployment change through GitOps.

Acceptance requires `https://docs.iambarton.com/pulse/` to serve the book. A local build or uploaded artifact alone is not publication.

## 7. Verification and acceptance gates

| Gate | Required evidence |
| --- | --- |
| Book build | Pinned reproducible build; no missing chapter, asset, internal link, or invalid Mermaid graph |
| Reading experience | Desktop/mobile, keyboard navigation, search, copyable commands, dark/light themes, accessible diagrams |
| Clean manual installation | Entire installation followed from the prose on a new isolated cluster without Make/demo orchestration |
| Runtime ownership | Learner can identify resources they applied and those created by reconciliation |
| Every supported canary shape | Baseline, intended failure, observed evidence, and recovery recorded |
| Real models | Model artifacts/native library verified; real Potion and tagged ONNX tests execute without skips |
| Model reasoning | Recorded scores and thresholds; negative controls; no unsupported causality or LLM claims |
| Actions | Correlated incident IDs and exact action counts; credentials absent from published transcripts |
| Manual customization | Changed assertion and model threshold produce explainable effects and can be undone |
| Contributor path | A real code/test change is built and exercised through the documented steps |
| Portability | Separate Docker/Podman results; any unavailable platform explicitly marked untested |
| Production site | Requested HTTPS URL and nested paths verified, with rollback documented |

Use executable example files and a separate verification harness to keep commands and YAML checked in CI. That harness may automate validation for maintainers, but learners must be able to complete the guide manually. Test the prose's actual order and starting assumptions, not only the helper scripts.

The existing validation baseline includes ten demo scenarios, unit/envtest checks, real model tests, and two manager E2E specs on Podman. Reuse that evidence where applicable, but it does not validate the newly written manual chapters, Docker, external providers, or the published site.

## Delivery sequence

1. Merge this planning PR after its feature-branch dependency is resolved.
2. Land the book scaffold, source inventory, navigation, architecture contract, and build checks.
3. Land the manual installation and deterministic canary chapters with recorded clean-cluster validation.
4. Land model, incident, action, recovery, and contributor chapters with the experiment matrix and evidence.
5. Migrate/refine operations, quick start, API reference, and architecture diagrams; preserve existing links.
6. Add the docs hub in `homelab/apps/docs_site/` and linked image-build/GitOps changes, configure `docs.iambarton.com`, publish `/pulse/`, and verify the live site.

Do not mark a phase complete with placeholder chapters, omitted model prerequisites, untested copy/paste commands, or unavailable evidence represented as a successful result.

## Design references

- [The Kubebuilder Book](https://book.kubebuilder.io/): requested reading/navigation reference.
- [mdBook documentation](https://rust-lang.github.io/mdBook/): proposed generator, search, theme, and extension capabilities.
- [Homelab applications](https://github.com/bryanbarton525/homelab/tree/main/apps): required site location; existing `iambarton_site` supplies the application/image/GitOps convention.

Implementation should recheck toolchain and hosting documentation when selecting exact versions and deployment settings.
