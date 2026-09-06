# Cloud Agent base image for the Pulse Kubebuilder operator.
#
# Bakes in the stable, slow-to-install toolchain:
#   - Official Go (matches go.mod; avoids the auto-downloaded module-cache
#     toolchain that ships without the `covdata` tool `go test -cover` needs)
#   - Docker Engine (Docker-in-Docker) for building images and running Kind
#   - kind / kubectl / kubebuilder
#   - fuse-overlayfs (the VM root fs is overlay, so Docker cannot use overlay2)
#
# Per-repo dependency setup lives in install.sh; the Docker daemon is started
# per-boot by start.sh.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates curl git make sudo gnupg lsb-release \
        uidmap iptables \
    && rm -rf /var/lib/apt/lists/*

# fuse-overlayfs: its postinst can exit non-zero when /dev/fuse is absent at
# build time, but the binary is installed and works at runtime. Tolerate that.
RUN apt-get update \
    && (apt-get install -y --no-install-recommends fuse-overlayfs || true) \
    && rm -rf /var/lib/apt/lists/* \
    && test -x /usr/bin/fuse-overlayfs

# Official Go toolchain (pinned to go.mod's version).
ARG GO_VERSION=1.26.1
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tar.gz \
    && tar -C /usr/local -xzf /tmp/go.tar.gz \
    && rm /tmp/go.tar.gz
ENV PATH=/usr/local/go/bin:/usr/local/bin:$PATH
# Use the installed Go instead of auto-downloading a toolchain (that download
# lacks `covdata`, breaking `go test -coverprofile`).
ENV GOTOOLCHAIN=local

# Docker Engine + Buildx (Docker-in-Docker).
RUN install -m 0755 -d /etc/apt/keyrings \
    && curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg \
    && chmod a+r /etc/apt/keyrings/docker.gpg \
    && echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu noble stable" \
        > /etc/apt/sources.list.d/docker.list \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        docker-ce docker-ce-cli containerd.io docker-buildx-plugin \
    && rm -rf /var/lib/apt/lists/*

# kind, kubectl, kubebuilder.
RUN curl -fsSLo /usr/local/bin/kind "https://kind.sigs.k8s.io/dl/latest/kind-linux-amd64" \
    && chmod +x /usr/local/bin/kind \
    && KUBECTL_VERSION="$(curl -Ls https://dl.k8s.io/release/stable.txt)" \
    && curl -fsSLo /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" \
    && chmod +x /usr/local/bin/kubectl \
    && curl -fsSLo /usr/local/bin/kubebuilder "https://go.kubebuilder.io/dl/latest/linux/amd64" \
    && chmod +x /usr/local/bin/kubebuilder

# Run as the default non-root `ubuntu` user (uid 1000 in ubuntu:24.04) with
# passwordless sudo (needed to start dockerd) and docker group membership.
RUN groupadd -f docker \
    && usermod -aG docker ubuntu \
    && echo 'ubuntu ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/90-ubuntu \
    && chmod 0440 /etc/sudoers.d/90-ubuntu

USER ubuntu
ENV GOTOOLCHAIN=local
