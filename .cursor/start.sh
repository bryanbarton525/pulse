#!/usr/bin/env bash
# Per-boot startup: bring up the Docker daemon (Docker-in-Docker) so image
# builds and Kind clusters work. Idempotent and safe across restarts.
set -euo pipefail

SOCK=/var/run/docker.sock

if sudo -n docker -H "unix://${SOCK}" info >/dev/null 2>&1; then
  echo "Docker already running"
else
  echo "Starting dockerd (fuse-overlayfs storage driver)..."
  # The VM root filesystem is overlay, so the overlay2 driver cannot mount
  # overlay-on-overlay; fuse-overlayfs works in this nested environment.
  sudo -n nohup dockerd \
      --storage-driver=fuse-overlayfs \
      --host="unix://${SOCK}" \
      >/tmp/dockerd.log 2>&1 &

  for _ in $(seq 1 60); do
    if sudo -n docker -H "unix://${SOCK}" info >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
fi

if ! sudo -n docker -H "unix://${SOCK}" info >/dev/null 2>&1; then
  echo "ERROR: dockerd did not become ready; see /tmp/dockerd.log" >&2
  tail -n 20 /tmp/dockerd.log >&2 || true
  exit 1
fi

# Let the non-root user talk to the daemon without sudo.
sudo -n chmod 666 "${SOCK}" || true

if docker info >/dev/null 2>&1; then
  echo "Docker ready and usable by $(whoami)"
else
  echo "ERROR: docker socket not usable by $(whoami)" >&2
  exit 1
fi
