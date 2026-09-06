#!/usr/bin/env bash
# Repository bootstrap for the Pulse operator. Idempotent: safe to re-run.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

export PATH=/usr/local/go/bin:${PATH}
# Never auto-download a toolchain: the module-cache toolchain omits `covdata`,
# which breaks `go test -coverprofile`. The image ships the matching Go release.
export GOTOOLCHAIN=local

echo "==> Go: $(go version)"

echo "==> Downloading Go module dependencies"
go mod download

echo "==> Generating manifests + deepcopy and building binaries"
make manifests generate
make build build-proberunner

echo "==> Provisioning envtest binaries for 'make test'"
make setup-envtest

echo "==> install.sh complete"
