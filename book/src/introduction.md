# The Pulse Book

Pulse turns a monitoring contract into a Kubernetes resource. An HTTP canary can require a particular status and response text; journey, MCP, and gRPC checks add protocol-specific contracts. Optional intelligence detects changes that those contracts do not express and helps group failures into incidents.

This book is being built in stages. The component overview and isolated laboratory setup are the first manual learning chapters. Installation, model experiments, and failure-injection chapters are still being developed; the current book is not yet a complete installation course. Progress and the handoff checklist live in [plan.md](https://github.com/bryanbarton525/pulse/blob/codex/pulse-book/plan.md).

Choose a path according to what you need:

- Start with [the components](learn/components.md) to understand what runs and who manages it, then [create an isolated laboratory](learn/environment.md).
- Use [operations](operations.md) when you already have an installation to inspect.
- Use [development](development.md) when changing Pulse code.
- The existing automated quick start is `docs/quick-start.html` in the repository. Open that file locally for the narrated demo while the manual course is under construction.

The manual course will use direct commands and explicit manifests. You will inspect each resource and observe reconciliation. A model score, incident group, or generated investigation is evidence to interpret; it is not a guarantee of the actual cause of a failure.

## What is real in the local demonstration?

Protocol requests execute against local test applications. Potion and MiniLM use actual model weights. Latency detection uses statistics. The demonstration's generative investigation endpoint is a deterministic fixture: it records a real request and returns a prepared response. Slack and observability requests go to that same local recording sink. No external provider credentials are required for the demonstration.
