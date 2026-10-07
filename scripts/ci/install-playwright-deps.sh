#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright The Muster Authors

set -euo pipefail

# Configure apt with retries and timeouts
sudo tee /etc/apt/apt.conf.d/99github-actions-muster >/dev/null <<EOF
Acquire::Retries 3;
Acquire::http::Timeout 30;
Acquire::https::Timeout 30;
EOF

# Install Playwright dependencies with per-attempt timeout
for attempt in 1 2 3; do
  echo "Installing Playwright dependencies (attempt $attempt/3)..."
  if timeout 150 pnpm --dir web exec playwright install --with-deps chromium; then
    echo "Playwright dependencies installed successfully"
    exit 0
  fi
  if [ "$attempt" -lt 3 ]; then
    echo "Install failed, retrying..."
    sleep 10
  fi
done

echo "Failed to install Playwright dependencies after 3 attempts"
exit 1
