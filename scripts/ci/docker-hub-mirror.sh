#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright The Muster Authors

# Makes the runner's Docker daemon pull Docker Hub images through Google's pull-through cache, because Docker Hub
# limits unauthenticated pulls per address and the shared GitHub-hosted runners exhaust that limit. The mirror needs
# no credentials and serves the same manifests, so image digests do not change. Only Docker Hub (docker.io) pulls are
# mirrored. When the mirror lacks an image or fails, the daemon falls back to Docker Hub by itself. Image references in
# the code, the compose example and the chart stay on docker.io.
#
# The runner image is logged in to Docker Hub with a shared account whose pulls are the ones being limited. The daemon
# sends those credentials to the mirror too, and the mirror answers "unauthorized: authentication failed" and the
# daemon falls back to the limited Docker Hub. The script logs the client out, so the mirror takes anonymous pulls.

set -euo pipefail

mirror="https://mirror.gcr.io"
config=/etc/docker/daemon.json

sudo mkdir -p "$(dirname "$config")"
existing='{}'
if [ -s "$config" ]; then
  existing=$(sudo cat "$config")
fi

echo "$existing" | jq --arg mirror "$mirror" \
  '.["registry-mirrors"] = ((.["registry-mirrors"] // []) + [$mirror] | unique)' |
  sudo tee "$config" >/dev/null

sudo systemctl restart docker

for _ in $(seq 1 30); do
  if docker info >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

docker info --format '{{range .RegistryConfig.Mirrors}}registry mirror: {{.}}{{"\n"}}{{end}}'
docker logout >/dev/null 2>&1 || true
