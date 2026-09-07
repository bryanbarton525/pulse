# Pulse Book implementation plan

Status: implementation started. Planning PR #3 merged into `feat/model-intelligence`. The implementation branch is `codex/pulse-book`, based on `46eb396`. The documentation site has not been deployed.

## Current handoff checkpoint

Active draft PR: [#4 — implement Pulse Book and manual learning course](https://github.com/bryanbarton525/pulse/pull/4). First implementation commit: `2ad32a3`.

### Cursor handoff — latest checkpoint

Browser verification now passed for the simplified component diagram, both diagrams in light and Navy themes, interactive rerendering back to light, MiniLM search navigation under `/book/`, and the 400px mobile layout. No Mermaid render error was set; highlight.js emits only benign unknown-language warnings before Mermaid replaces the source blocks.

The model preparation increment pins Potion and MiniLM Hugging Face repository commits and verifies SHA-256 for all four downloaded inputs. A new manual `learn/prepare-models.md` chapter exposes the exact downloads, hashes, Potion conversion format, licenses, reset procedure, and the distinction between real model tests and skips. Its build and real Potion execution still need to be completed at this checkpoint; do not report MiniLM runtime validation until the tagged native ONNX test runs. The current machine has no Kind context or container runtime, so the target and runner port-forward commands remain outstanding.

Branch: `codex/pulse-book`; keep pushing increments to draft PR #4. The PR base remains `feat/model-intelligence`, where the planning PR merged. Do not assume `main` contains its dependencies.

New files: manual `learn/install.md` and `learn/first-canary.md`, a reviewed Kustomize install overlay, and explicit target/canary YAML under `book/examples/`. `.dockerignore` now excludes the book so npm dependencies do not enter Go image build contexts.

Executed on the isolated `kind-pulse-book` cluster with Podman: built and loaded controller/runner/target `localhost/pulse-*:book-v1` images; installed and waited for all three CRDs; applied rendered install overlay; manager and runner became Ready; deployed `book-shop/catalogue`; observed Healthy with HTTP 200 and matching `items`. Patched the contract to `a-marker-that-is-not-present`; observed Unhealthy with HTTP 200 and that exact failure message. Recovery is now verified: reapplied the original canary, the bounded Healthy wait passed, and a separate read confirmed `containsText=items`, `phase=Healthy`, and `Got expected status 200 and matched response text`. The chapter now explains asynchronous mounted-ConfigMap propagation before the next probe.

Browser evidence: both Mermaid diagrams render without overlapping labels in the architecture chapter. Nested navigation and local assets load; theme changes rerender the diagrams; MiniLM search and the 400px mobile layout passed. Book build and all links across 14 generated pages passed before adding the model chapter. The preview server may still be running in the `pulse-book-preview` tmux session.

Next: build and link-check the model chapter, execute its pinned fetch/conversion and real Potion tests, execute the first-canary chapter's target/runner port-forward inspection commands when a lab runtime is available, then prepare the incident-engine image and tagged MiniLM validation. Continue manual canary variants and model experiments. Do not report installation of the later incident-engine/model image as completed: the completed Kubernetes increment only installs deterministic monitoring. No homelab implementation yet.

Cursor bootstrap: `npm ci --prefix book --ignore-scripts`, `npm run --prefix book assets`, `mdbook build` with mdBook 0.4.52, then `python3 book/check-links.py`. Temporary local mdBook executable was `/private/tmp/pulse-mdbook-0.4.52/mdbook`. The preview server may need restarting; serve `book/build` as `/book/`, not the repository itself. Use explicit Kubernetes contexts. `pulse-demo` is separate from this lab. Preserve ignored graph/editor files; do not stage generated assets or bypass the large-file hook.

### Completed foundation and validation

Added Mermaid 11.17.2 with npm lockfile and locally generated assets, component and sequence diagrams, CRD/journey reference includes, and a generated-HTML link/fragment checker wired into CI. Updated stale development prerequisites and scaffolding guidance. Dependency installation reported zero vulnerabilities at that checkpoint.

Pre-commit recovery: generated `graphify-out/graph.json` and `graph.html` were accidentally staged and exceeded the large-file hook limit. Exclude local graph output and `cmd/homelab.code-workspace` from commits; preserve both on disk. Generated Mermaid assets remain ignored and are reconstructed from the lockfile during builds.

All four pre-commit hooks passed after removing local generated files from staging. The manual environment run verified node Ready, no preinstalled CRDs, and the bounded CoreDNS rollout check. `pulse-book` now contains the deterministic installation and recovered first canary; `pulse-demo` is a separate existing cluster. Kind changed the current context, so continue using explicit contexts.

Completed in the first implementation increment:

- Added `book.toml` with `/book/` site URL, chapter navigation, search through mdBook defaults, and light/dark themes; pinned mdBook 0.4.52.
- Added a substantive component/ownership overview and manual isolated Kind environment chapter. Neither uses Make or demo orchestration.
- Added introduction, book contributor instructions, and includes of canonical operations/development pages; unfinished course chapters are not listed as completed content.
- Added a CI book build and downloadable preview artifact. This workflow does not publish the production site.
- Ignored generated `book/build/` output.

Build tooling: official mdBook 0.4.52 for macOS arm64. Existing runtime test suites were not repeated for these documentation-only increments. Manual lab evidence is recorded above; it is not full course or model validation.

Next agent: start with `git status`, this checkpoint, and the implementation PR. Preserve unrelated untracked `cmd/homelab.code-workspace` and `graphify-out/`.

Immediate next work, in order:

1. Internal links and bundled Mermaid assets are implemented. Verify diagram rendering and theme switching in a browser, then replace the incomplete legacy architecture diagram with the verified book diagrams.
2. Validate desktop/mobile rendering and `/book/` nested-path behavior. Verify the CI preview build on the PR; add release-asset integrity checking before production publication.
3. Environment creation, CoreDNS readiness, deterministic image builds/install, and the first canary failure/recovery passed on Podman/Kind with Kubernetes v1.37.0 on arm64. Validate the outstanding port-forward inspection steps, then write and execute the manual model preparation and incident-engine installation chapters.
4. Continue the chapter sequence below; update this checkpoint at each meaningful increment with files, test results, limitations, and next actions.
5. Implement the homelab site and GitOps PR only after the content/build contract is usable. No homelab files or live cluster settings have been changed in this increment.

Known gaps: book is incomplete; revised diagram layout, interactive theme behavior, search, and mobile rendering need verification; model/protocol/action/contributor course chapters remain to be written and executed; Docker is untested; production hosting is not implemented. The manual target/runner port-forward steps still need execution. Do not report the book or site complete based on a successful mdBook build.

Target: `https://pulse.iambarton.com/book/`.

Hosting repository: [bryanbarton525/homelab](https://github.com/bryanbarton525/homelab/tree/main/apps). The Pulse site application belongs at `homelab/apps/pulse_site/`; deployment resources follow that repository's separate `clusters/` convention. Documentation stays alongside Pulse code, and the site consumes a pinned documentation revision to avoid divergent copies.

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
apps/pulse_site/                    # Dockerfile, static server configuration, pinned book source, runbook
.github/workflows/pulse-site-build.yml
clusters/namespace-pulse-site/      # Kustomize deployment, Service, routing, TLS configuration
clusters/argocd/pulse-site.yaml     # Argo CD Application
```

These are proposed new paths following the existing `apps/iambarton_site`, image-build workflow, namespace manifests, and Argo CD Application pattern. Keep application source under `apps/` and cluster configuration under `clusters/`.

- Use a collapsible chapter sidebar, previous/next navigation, local search, copyable code blocks, deep links, light/dark themes, and readable mobile layouts.
- Preserve Pulse branding while adopting the restrained reading layout of the Kubebuilder Book. Do not copy its logos or project-specific content.
- Bundle Mermaid rendering assets with the site; diagrams must remain readable in both themes and have a prose equivalent.
- Configure and test all navigation, images, fonts, search assets, and diagrams under `/book/`, including direct navigation to nested chapters.
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

## 6. Publish at pulse.iambarton.com/book/

1. Implement the site at `homelab/apps/pulse_site/`. Follow the inspected homelab pattern: GitHub Actions builds application images into GHCR, Argo CD reconciles a Kustomize directory under `clusters/`, and Gateway API HTTPRoutes expose the site. GitHub Pages is not the deployment target.
2. Keep canonical book sources in Pulse. Pin a Pulse commit or verified artifact in the homelab site build; record it in the image metadata and book. Define an explicit promotion PR that updates this pin and the deployed image digest together. Do not fetch a moving branch at container startup.
3. Serve the generated static content beneath `/book/` in the site container. Test `/book` redirection and nested routes without stripping the prefix incorrectly. Preserve any existing content at the hostname root.
4. Add `homelab/.github/workflows/pulse-site-build.yml` with validation and preview checks for PRs and GHCR publication from merged site changes. Use immutable image references for deployment. Pulse's docs workflow validates/builds the book; homelab owns site packaging and deployment.
5. Add the namespace, Deployment, Service, readiness/liveness probes, resource settings, and Kustomize configuration under `clusters/namespace-pulse-site/`, plus `clusters/argocd/pulse-site.yaml`. Check app-of-apps discovery and repository instructions before wiring it in; merging an automatically synced Argo CD Application can initiate a deployment.
6. Inspect the actual gateway, certificate issuer, external-dns/Cloudflare configuration, and existing `pulse.iambarton.com` record before defining host routing and TLS. The inspected portal uses a Gateway API route and external-dns hostname annotation; reuse the applicable convention without copying portal-specific names or credentials. DNS configures the hostname, while the server/route handles `/book/`.
7. Open linked Pulse and homelab PRs, documenting source revision, image digest, routing, and rollout order. Verify HTTPS, `/book` to `/book/` handling, nested chapter URLs, search, diagrams, old documentation links, and cache behavior. Roll back by reverting the pinned source/image deployment change through GitOps.

Acceptance requires the requested URL to serve the book. A local build or uploaded artifact alone is not publication.

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
6. Add the site application in `homelab/apps/pulse_site/` and linked image-build/GitOps changes, configure the verified host, publish `/book/`, and verify the live site.

Do not mark a phase complete with placeholder chapters, omitted model prerequisites, untested copy/paste commands, or unavailable evidence represented as a successful result.

## Design references

- [The Kubebuilder Book](https://book.kubebuilder.io/): requested reading/navigation reference.
- [mdBook documentation](https://rust-lang.github.io/mdBook/): proposed generator, search, theme, and extension capabilities.
- [Homelab applications](https://github.com/bryanbarton525/homelab/tree/main/apps): required site location; existing `iambarton_site` supplies the application/image/GitOps convention.

Implementation should recheck toolchain and hosting documentation when selecting exact versions and deployment settings.
