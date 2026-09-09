# Documentation inventory and provenance

The book is the connected reading experience. Existing repository entry points remain available so old links do not break; Markdown sources are included rather than copied when they remain canonical.

## Source inventory

| Existing source | Canonical destination | Treatment |
| --- | --- | --- |
| `README.md` | Welcome, quick start, and links into the book | Keep concise repository overview and automated demo entry |
| `DEBUG_GUIDE.md` | Development, testing, and operations chapters | Preserve path as a redirect page; remove duplicated tool versions and commands |
| `docs/quick-start.html` | Book quick start | Preserve the standalone automated-tour page; manual chapters explain its underlying commands |
| `docs/architecture-summary.md` | Components chapter | Preserve concise standalone summary and link it to detailed architecture |
| `docs/architecture.md` | Architecture reference | Include directly; use the verified Mermaid component graph |
| `docs/reconciliation-design.md` | Reconciliation design | Include directly |
| `docs/scaling.md` | Scaling and sharding | Include directly |
| `docs/operations.md` | Operations | Include directly |
| `docs/helm.md` | Helm deployment | Include directly |
| `docs/testing-and-validation.md` | Testing and validation | Include directly |
| `docs/development.md` | Development | Include directly |
| `docs/crd-design.md` | CRD design | Include directly |
| `docs/http-journey-canary.md` | HTTP journey reference | Include directly; course chapter owns the executed exercise |
| `docs/model-intelligence.html` | Intelligence course chapters | Preserve the visual deep dive; course chapters own reproducible commands and current evidence |
| `docs/demo-refactoring-handoff.md` | Maintainer history | Keep outside navigation; it records implementation history rather than user guidance |
| `config/samples/` | CRD and operations references | Keep executable samples beside generated CRDs |
| `hack/demo/` | Quick-start harness and course fixtures | Keep maintainer automation; manual chapters expose equivalent individual commands |

## Command and diagram provenance

| Book material | Runtime/source truth | Verification |
| --- | --- | --- |
| Component ownership and reconciliation sequence | `cmd/main.go`, `internal/controller/canary_controller.go`, `workloads.go`, `status_syncer.go` | Controller/envtest suite plus browser-rendered diagrams |
| HTTP and journey commands | `internal/proberunner/http.go`, journey implementation, `cmd/demotarget/main.go` | Unit tests, executable YAML parsing, manual target/result inspection |
| MCP exchange | MCP runner implementation and `cmd/demotarget` JSON-RPC routes | Protocol tests and manual initialize/initialized/tools-list exchange |
| gRPC health | gRPC runner and standard `grpc.health.v1` fixture | gRPC tests, pinned `grpc-health-probe`, and the temporary Go health client |
| Potion conversion and drift | `hack/fetch-models.py`, `internal/embed/potion.go`, anomaly drift code | Pinned hashes, real Potion tests, model-loaded log, measured score |
| MiniLM correlation and novelty | `internal/embed/onnx_enabled.go`, incident engine and aggregator | Tagged real ONNX tests, engine image model-loaded log, scenario merge evidence |
| Actions | policy action resolver, dispatcher, `hack/demo/05-sink.yaml` | Sink request logs filtered by incident ID and exact action counts |
| Sharding and freshness | shard parser/ownership, controller status sharding | Unit tests, per-ordinal result inspection, stale-shard experiment |
| Book rendering | `book.toml`, npm lockfile, bundled Mermaid renderer | mdBook build, link/fragment checker, desktop/mobile/theme/search browser checks |

Captured values such as Pod names, timestamps, durations, IP addresses, incident IDs, similarity scores, and cumulative metric counts are variable. Chapters identify them as observations rather than stable expected literals.
