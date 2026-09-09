# Pulse debugging guide

This path is preserved for existing links. Current debugging guidance is maintained in:

- [Development](docs/development.md) for tool setup, local execution, code generation, and debugger workflows
- [Testing and validation](docs/testing-and-validation.md) for unit, envtest, and isolated Kind checks
- [Operations](docs/operations.md) for in-cluster logs, live results, metrics, model health, sharding, and recovery
- [The Pulse Book](book/src/introduction.md) for the complete manual laboratory and contributor exercise

Use explicit Kubernetes contexts in diagnostic commands. The book laboratory uses `kind-pulse-book`; the automated demo uses `kind-pulse-demo`; end-to-end tests create their own isolated cluster. Do not run course or E2E cleanup against a shared development or production context.
