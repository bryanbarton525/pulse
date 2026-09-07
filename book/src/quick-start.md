# Quick start

Use the automated tour when you want to see the complete system before working through each command manually. It creates a dedicated `pulse-demo` Kind cluster, builds all four images, installs every fixture and policy, and runs asserted scenarios:

```sh
make demo-up
make demo-tour-paced
make demo-lab
```

The default Kubernetes context is `kind-pulse-demo`; override `DEMO_CLUSTER` only with another isolated lab name. The scenario driver verifies deterministic HTTP, journey, MCP, and gRPC failures plus Potion drift, latency, declared and model-only correlation, novelty replay, and action counts. It is a demonstration and maintainer harness, not a substitute for understanding the underlying resources.

Inspect the live state between scenarios:

```sh
kubectl --context kind-pulse-demo -n shop get httpcanaries,grpccanaries
kubectl --context kind-pulse-demo -n pulse-system get deploy,sts,pods
kubectl --context kind-pulse-demo -n pulse-system get --raw \
  '/api/v1/namespaces/pulse-system/services/http:pulse-incident-engine:9090/proxy/results' |
  python3 -m json.tool
```

Run `make demo-restore` to return fixture behavior to healthy, and `make demo-down` to delete only the demo cluster. The [manual laboratory](learn/environment.md) uses the separate `pulse-book` cluster and exposes every build, manifest, mutation, inspection, and recovery step.

For ongoing administration, use the [operations guide](operations.md). For the full automated command reference and expected narration, the preserved [legacy quick-start page](https://github.com/bryanbarton525/pulse/blob/main/docs/quick-start.html) remains available.
