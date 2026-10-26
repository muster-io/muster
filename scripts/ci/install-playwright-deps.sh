#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright The Muster Authors

# Installs the SPA dependencies, the Chromium of the pinned Playwright and the system packages Chromium needs, taking
# the packages' .deb files from a cache instead of the Ubuntu mirror, which at times serves some of them at a few kB/s.
#
# A job runs it between two cache steps. `install-playwright-deps.sh key` writes the key prefixes, which name the OS
# version, the architecture and the Playwright version; actions/cache/restore restores the newest cache under them, or
# under the OS prefix alone after a Playwright upgrade, into ~/.cache/playwright-debs, and the script gets its key in
# PLAYWRIGHT_DEBS_RESTORED. The script copies the cached files into apt's archive directory before apt-get runs, so apt
# downloads only the packages they lack, such as an update published since. Afterwards the directory keeps the files of
# the installed versions, and their key ends in a hash of their names. The script writes that key, and
# actions/cache/save stores the directory under it, only when it differs from the restored one: when the run
# downloaded or dropped a file.
#
# Each attempt has its own timeout and is retried. The system packages are installed by `playwright install-deps` run
# as root under timeout, so that the timeout also stops apt-get, which a timeout run as the runner user cannot signal.

set -euo pipefail

cache=$HOME/.cache/playwright-debs
archives=/var/cache/apt/archives
attempt_timeout=150

prefixes() {
  local os playwright
  os=$(. /etc/os-release && echo "$ID$VERSION_ID")
  playwright=$(sed -n 's/^ *"playwright": *"\([^"]*\)".*/\1/p' web/package.json)
  if [ -z "$playwright" ]; then
    echo "no version of playwright in web/package.json" >&2
    exit 1
  fi
  os_prefix="playwright-debs-$os-$(dpkg --print-architecture)-"
  prefix="${os_prefix}playwright-$playwright-"
}

debs() {
  find "$1" -maxdepth 1 -name '*.deb' -printf '%f\n' | LC_ALL=C sort
}

retry() {
  local what=$1
  shift
  for attempt in 1 2 3; do
    echo "Installing $what (attempt $attempt/3)..."
    if "$@"; then
      return 0
    fi
    if [ "$attempt" -lt 3 ]; then
      echo "Installing $what failed or took longer than ${attempt_timeout}s, retrying..."
      sleep 10
    fi
  done
  echo "Failed to install $what after 3 attempts"
  return 1
}

install_browser() {
  timeout -k 10 "$attempt_timeout" pnpm --dir web exec playwright install chromium
}

install_system_packages() {
  sudo dpkg --configure -a
  sudo timeout -k 10 "$attempt_timeout" "$(command -v node)" web/node_modules/playwright/cli.js install-deps chromium
}

prefixes
if [ "${1:-}" = key ]; then
  echo "os-prefix=$os_prefix"
  echo "prefix=$prefix"
  exit 0
fi

sudo tee /etc/apt/apt.conf.d/99github-actions-muster >/dev/null <<EOF
Acquire::Retries 3;
Acquire::http::Timeout 30;
Acquire::https::Timeout 30;
APT::Keep-Downloaded-Packages "true";
Binary::apt-get::APT::Keep-Downloaded-Packages "true";
EOF

mkdir -p "$cache"
cached=$(debs "$cache" | wc -l)
if [ "$cached" -gt 0 ]; then
  echo "Playwright system packages: cache hit, $cached .deb files restored from ${PLAYWRIGHT_DEBS_RESTORED:-the cache}"
  sudo cp "$cache"/*.deb "$archives"/
else
  echo "Playwright system packages: cache miss, apt downloads every package from the mirror"
fi
before=$(debs "$archives")

retry "the SPA dependencies and Chromium" install_browser
retry "the Playwright system packages" install_system_packages
echo "Playwright dependencies installed successfully"

downloaded=$(LC_ALL=C comm -13 <(echo "$before") <(debs "$archives"))
if [ -z "$downloaded" ]; then
  echo "Playwright system packages: no .deb file downloaded from the mirror"
else
  size=$(cd "$archives" && echo "$downloaded" | xargs du -ck | tail -n 1 | cut -f 1)
  echo "Playwright system packages: $(echo "$downloaded" | wc -l) .deb files (${size} kB) downloaded from the mirror:"
  echo "$downloaded" | sed 's/^/  /'
fi

installed=$(dpkg-query -W -f='${db:Status-Abbrev}${Package}_${Version}_${Architecture}\n' | sed -n 's/^ii *//p')
rm -rf "$cache"
mkdir -p "$cache"
for deb in "$archives"/*.deb; do
  [ -e "$deb" ] || continue
  if grep -Fxq "$(dpkg-deb --show --showformat='${Package}_${Version}_${Architecture}' "$deb")" <<<"$installed"; then
    cp "$deb" "$cache"/
  fi
done

count=$(debs "$cache" | wc -l)
key=$prefix$(debs "$cache" | sha256sum | cut -c1-16)
if [ "$count" -eq 0 ]; then
  echo "Playwright system packages: none downloaded or restored, nothing to cache"
elif [ "$key" = "${PLAYWRIGHT_DEBS_RESTORED:-}" ]; then
  echo "Playwright system packages: the cache $key is current, nothing to save"
else
  echo "Playwright system packages: $count .deb files of the installed versions to save as the cache $key"
  echo "key=$key" >>"${GITHUB_OUTPUT:-/dev/null}"
fi
