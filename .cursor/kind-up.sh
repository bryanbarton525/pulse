#!/usr/bin/env bash
# Bring up a Kind cluster + local image registry that work inside the Cloud
# Agent's nested-container VM, applying three workarounds this environment
# requires:
#
#   1. containerd "native" snapshotter in the node -- the VM root fs is overlay
#      and Docker uses fuse-overlayfs, so the node cannot mount overlay-on-
#      overlay. "native" uses plain copies and works.
#   2. A local registry on the "kind" Docker network -- that network has no
#      internet egress, and `kind load` cannot unpack images under the native
#      snapshotter, so images are pulled by the node's CRI from a local
#      registry instead.
#   3. kube-proxy in "nftables" mode -- the bundled iptables-nft `iptables-
#      restore` fails here, so ClusterIP routing needs the native nftables
#      proxier.
#
# Usage:
#   .cursor/kind-up.sh                 # create cluster "pulse-dev" + registry
#   CLUSTER=my-cluster .cursor/kind-up.sh
#
# After it prints "Cluster ready", push images to localhost:5001/... and
# reference them from that registry in your manifests.
set -euo pipefail

CLUSTER="${CLUSTER:-pulse-dev}"
REG_NAME="${REG_NAME:-kind-registry}"
REG_PORT="${REG_PORT:-5001}"

echo "==> Ensuring Docker is available"
docker info >/dev/null 2>&1 || { echo "Docker is not running; run .cursor/start.sh first" >&2; exit 1; }

if ! kind get clusters 2>/dev/null | grep -qx "${CLUSTER}"; then
  echo "==> Creating Kind cluster '${CLUSTER}' (native snapshotter)"
  cat <<EOF | kind create cluster --name "${CLUSTER}" --config=- --wait 120s
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
  - |-
    [plugins."io.containerd.grpc.v1.cri".containerd]
      snapshotter = "native"
EOF
else
  echo "==> Kind cluster '${CLUSTER}' already exists"
fi

NODE="${CLUSTER}-control-plane"

echo "==> Ensuring local registry '${REG_NAME}' on the kind network"
if [ "$(docker inspect -f '{{.State.Running}}' "${REG_NAME}" 2>/dev/null || true)" != "true" ]; then
  docker rm -f "${REG_NAME}" >/dev/null 2>&1 || true
  docker run -d --restart=always --name "${REG_NAME}" \
    --network kind -p "127.0.0.1:${REG_PORT}:5000" registry:2 >/dev/null
fi

echo "==> Pointing node containerd at the registry (localhost:${REG_PORT})"
docker exec "${NODE}" bash -c "
set -e
grep -q 'config_path' /etc/containerd/config.toml || cat >> /etc/containerd/config.toml <<'CFG'

[plugins.\"io.containerd.grpc.v1.cri\".registry]
  config_path = \"/etc/containerd/certs.d\"
CFG
mkdir -p '/etc/containerd/certs.d/localhost:${REG_PORT}'
cat > '/etc/containerd/certs.d/localhost:${REG_PORT}/hosts.toml' <<'HOSTS'
server = \"http://${REG_NAME}:5000\"
[host.\"http://${REG_NAME}:5000\"]
  capabilities = [\"pull\", \"resolve\", \"push\"]
  skip_verify = true
HOSTS
kill -HUP \$(pidof containerd)
"

echo "==> Switching kube-proxy to nftables mode"
KUBECONF="$(kubectl -n kube-system get cm kube-proxy -o jsonpath='{.data.kubeconfig\.conf}')"
CFG="$(kubectl -n kube-system get cm kube-proxy -o jsonpath='{.data.config\.conf}')"
if ! echo "$CFG" | grep -qE '^mode: "?nftables"?'; then
  NEWCFG="$(echo "$CFG" | sed -E 's/^mode: .*/mode: "nftables"/')"
  kubectl -n kube-system create cm kube-proxy \
    --from-literal=config.conf="$NEWCFG" \
    --from-literal=kubeconfig.conf="$KUBECONF" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n kube-system rollout restart ds/kube-proxy >/dev/null
  kubectl -n kube-system rollout status ds/kube-proxy --timeout=90s
fi

echo "==> Cluster ready"
echo "    kubectl context : kind-${CLUSTER}"
echo "    local registry  : localhost:${REG_PORT}  (push images here)"
