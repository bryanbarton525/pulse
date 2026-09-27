# Existing k3s sandbox baseline

Source baseline: `a92eda010c2c3289c5cd79d9881552608d0ac203`.
This procedure adapts the checked-in demo without creating a Kind cluster.
It has not yet been validated on k3s. Do not use `demo-up`, `demo-install`,
`demo-deploy`, `demo-down`, or the Kind e2e targets on a shared cluster.

## Prerequisites and containment

Chief Architect must identify the sandbox context, approve the existing operator
handling these canaries, and provision an empty namespace dedicated to this run.
Proposed namespace: `orc3-pulse-a92eda01`; availability is unverified.
The renderer removes Namespace objects; it never contacts Kubernetes.
Do not install a second controller: the pinned manager watches across namespaces,
and setting `POD_NAMESPACE` only changes its infrastructure namespace.
Existing CRDs, controller, runner and incident-engine images must be compatible
with the pinned commit. Record their actual image digests and versions before
claiming a baseline. CRD, cluster RBAC, operator changes, image publishing and
registry access must be coordinated with Chief Architect.

Tide needs namespace-local get/list/watch/create/update/patch/delete for
Deployments, Services, ConfigMaps, the demo Secret, HttpCanaries, GrpcCanaries,
and AnomalyPolicies; get/list/watch Pods and Events, get pods/log, and patch
Deployments for the failure/recovery scenarios. Read-only operator workload/log
access and an approved way to inspect authenticated live results may be needed.
Do not request cluster-admin or dump Secrets into evidence.

## Access check (before any deployment)

Set `CTX` to the provided sandbox context, `NS=orc3-pulse-a92eda01`, and `OUT`
to a private evidence directory. Always specify context and namespace explicitly.
Record commands and exit codes; never record kubeconfig contents or tokens.

```bash
kubectl version --client -o yaml
kubectl --context "$CTX" version -o yaml
kubectl --context "$CTX" get namespace "$NS" -o name
kubectl --context "$CTX" -n "$NS" auth can-i --list
kubectl --context "$CTX" -n "$NS" get deploy,svc,cm,httpcanaries,grpccanaries,anomalypolicies
```

Stop if context identity, ownership, permission or emptiness checks fail. Record
server version (including k3s suffix), client version, source commit, image
digests, start time and namespace. Ask the sandbox owner for the k3s version if
not visible in the server version. Namespace isolation alone does not isolate
the shared operator's workload or policy processing; confirm capacity first.

## Render and deploy the demo

Use a registry-reachable demo-target image built from the pinned commit, provided
as a digest in `TARGET_IMAGE`. The sink uses the upstream demo's `python:3.12-alpine`
image; record its resolved digest too. Native model validation additionally
requires the appropriate model-enabled runner/engine images and model artifacts.

```bash
python3 hack/demo/render_k3s.py --namespace "$NS" \
  --target-image "$TARGET_IMAGE" > "$OUT/demo.yaml"
kubectl --context "$CTX" -n "$NS" apply --dry-run=server -f "$OUT/demo.yaml"
kubectl --context "$CTX" -n "$NS" apply -f "$OUT/demo.yaml"
for deployment in catalogue checkout search unrelated mcp orders-grpc sink; do
  kubectl --context "$CTX" -n "$NS" rollout status deployment/"$deployment" --timeout=300s
done
kubectl --context "$CTX" -n "$NS" get httpcanaries,grpccanaries -o yaml > "$OUT/baseline.yaml"
```

The renderer relocates targets, sink, policy, demo-only auth Secret and canaries
into the selected namespace; updates service DNS and policy references; and
changes target image pull policy from Never to IfNotPresent. It preserves the
gRPC service name `shop.Orders`, which is a protocol identifier, not a namespace.
Do not use the existing scenario/inspection scripts unchanged: they hardcode
`shop`, `pulse-demo` and `pulse-system`.

## HTTP, gRPC and MCP evidence

Within a bounded five-minute observation window, require fresh observations
(after deployment) and Healthy for `catalogue`, `orders`, and `mcp-tools`.
Capture CR status timestamps, messages and phases, plus authenticated live
runner/engine results using the operator owner's approved access method.
HTTP must report 200; gRPC must report successful health/SERVING (zero status can
be omitted in serialized CR status); MCP must negotiate and include `health.check`.
A Healthy phase without a fresh observation is insufficient.

For each protocol, inject one failure, observe a fresh Unhealthy result, restore,
and observe fresh Healthy before proceeding. Record timestamps and actual result
messages; do not infer success from rollout readiness.

```bash
# HTTP
kubectl --context "$CTX" -n "$NS" set env deployment/catalogue DEMO_BEHAVIOR=outage
# Capture fresh catalogue failure before restoring.
kubectl --context "$CTX" -n "$NS" set env deployment/catalogue DEMO_BEHAVIOR=healthy
# gRPC
kubectl --context "$CTX" -n "$NS" set env deployment/orders-grpc DEMO_BEHAVIOR=grpc-fail
# Capture fresh orders failure before restoring.
kubectl --context "$CTX" -n "$NS" set env deployment/orders-grpc DEMO_BEHAVIOR=healthy
# MCP
kubectl --context "$CTX" -n "$NS" set env deployment/mcp DEMO_BEHAVIOR=mcp-missing-tool
# Capture fresh mcp-tools failure before restoring.
kubectl --context "$CTX" -n "$NS" set env deployment/mcp DEMO_BEHAVIOR=healthy
```

On the first unexpected failure, stop subsequent scenarios and save the failing
command/exit code, canary YAML, namespace Events, target logs and relevant
controller/runner/engine logs with timestamps. Keep logs bounded (`--tail=200`),
redact sensitive content, and avoid Secret/config dumps. Diagnose before retrying.

## Cleanup

Delete only the resources in the exact rendered manifest, then verify their
absence. Do not delete the namespace, shared CRDs, operator or shared runner.
Record any finalizers or deletion errors for the owner.

```bash
kubectl --context "$CTX" -n "$NS" delete -f "$OUT/demo.yaml" --ignore-not-found --timeout=120s
kubectl --context "$CTX" -n "$NS" get deploy,svc,cm,httpcanaries,grpccanaries,anomalypolicies
kubectl --context "$CTX" -n "$NS" get secret demo-sink-auth -o name
```

The final Secret check should return NotFound. Preserve evidence separately;
never commit kubeconfigs, tokens or runtime customer data.
