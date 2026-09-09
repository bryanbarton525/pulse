# The Pulse Book

Pulse turns a monitoring contract into a Kubernetes resource. An HTTP canary can require a particular status and response text; journey, MCP, and gRPC checks add protocol-specific contracts. Optional intelligence detects changes that those contracts do not express and helps group failures into incidents.

The manual path covers the complete local lifecycle: isolated laboratory setup, model preparation, installation, HTTP/journey/MCP/gRPC checks, drift and latency detection, incident correlation and novelty, action dispatch, sharding, recovery, and a tested contribution. Production publication is a separate GitOps concern; a successful local course does not prove that a hosted deployment is current.

Choose a path according to what you need:

- Use the [quick start](quick-start.md) for the automated demonstration.
- Start with [the components](learn/components.md) to understand what runs and who manages it, then [create an isolated laboratory](learn/environment.md) for the full manual course.
- Use [operations](operations.md) when you already have an installation to inspect.
- Use [development](development.md) when changing Pulse code.

The manual course uses direct commands and explicit manifests. You inspect each resource and observe reconciliation. A model score, incident group, or generated investigation is evidence to interpret; it is not a guarantee of the actual cause of a failure.

## What is real in the local demonstration?

Protocol requests execute against local test applications. Potion and MiniLM use actual model weights. Latency detection uses statistics. The demonstration's generative investigation endpoint is a deterministic fixture: it records a real request and returns a prepared response. Slack and observability requests go to that same local recording sink. No external provider credentials are required for the demonstration.
