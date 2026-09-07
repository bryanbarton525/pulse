# Pulse Book implementation plan

Status: proposed implementation plan. This PR establishes the work and acceptance criteria; it does not deploy the documentation site.

Target: `https://pulse.iambarton.com/book/`.

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

1. Inspect existing domain ownership, hosting configuration, DNS, TLS termination, and any content already served at the hostname. The repository currently has no documentation publishing workflow; do not assume which hosting provider owns the domain.
2. Build a provider-independent static artifact served beneath `/book/`. Stage a preview first and test path handling without changing the hostname's root content.
3. Select the deployment adapter from actual hosting evidence: existing static hosting/reverse proxy, or GitHub Pages if its custom-domain ownership and routing fit. A DNS record controls a hostname, not a URL path; `/book/` routing must be handled by the host or artifact layout.
4. Add a workflow with separate validation, preview/artifact, and production publication responsibilities. Publish production only from the designated merged branch or release; keep PR preview credentials out of untrusted PR execution.
5. Record required external account settings, domain verification, DNS and TLS changes concretely. Apply them only where the available access and user's site scope permit; identify any remaining account-dependent step precisely.
6. Verify HTTPS, `/book` to `/book/` handling, nested chapter URLs, search, diagrams, links from old docs, cache invalidation, and a rollback to the previous static artifact.

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
6. Add preview/publication workflow, configure the verified host, publish `/book/`, and verify the live site.

Do not mark a phase complete with placeholder chapters, omitted model prerequisites, untested copy/paste commands, or unavailable evidence represented as a successful result.

## Design references

- [The Kubebuilder Book](https://book.kubebuilder.io/): requested reading/navigation reference.
- [mdBook documentation](https://rust-lang.github.io/mdBook/): proposed generator, search, theme, and extension capabilities.

Implementation should recheck toolchain and hosting documentation when selecting exact versions and deployment settings.
