#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright The Muster Authors

# Runs the commands of Quick start (docs/quick-start.md) exactly as the page shows them, in an empty directory, and
# checks that they reach a running Muster: /health/ready answers 200 and the bootstrap Admin signs in with the password
# the page wrote into .env. The commands are the shell blocks that follow a line "<!-- quickstart -->", in order.
#
# MUSTER_IMAGE, when set, is the image the compose example runs in place of the release image. QUICKSTART_REF, when set,
# is the Git ref the download reads in place of the release tag. Both are empty for the page exactly as written.
#
# The compose example publishes only port 8080, which serves the app and ingestion; /health/ready is on the internal
# listener (:8082), which it does not publish, and the Muster image has no shell. The check therefore asks for
# /health/ready from the PostgreSQL container of the same compose project, with its busybox wget.
#
# This is a check of a live system, so it waits in real time, with a deadline. It always removes the compose project
# with its volume, prints the logs of the containers when it fails, and never prints the passwords or the key.

set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
page="$root/docs/quick-start.md"
ready_timeout=${QUICKSTART_READY_TIMEOUT:-180}
ref=${QUICKSTART_REF:-}
if [ -n "${MUSTER_IMAGE:-}" ]; then
  export MUSTER_IMAGE
else
  unset MUSTER_IMAGE
fi

state=$(mktemp -d)
work=$(mktemp -d)
compose_dir=""
ok=0

# The directory of the downloaded compose file, wherever the commands of the page put it.
find_compose_dir() {
  local file
  file=$(find "$work" -maxdepth 3 -name docker-compose.yml -print -quit)
  if [ -n "$file" ]; then dirname "$file"; fi
}

cleanup() {
  local status=$?
  set +e
  if [ -z "$compose_dir" ]; then compose_dir=$(find_compose_dir); fi
  if [ -n "$compose_dir" ]; then
    if [ "$ok" -ne 1 ]; then
      echo "--- docker compose ps"
      (cd "$compose_dir" && docker compose ps -a)
      echo "--- docker compose logs"
      (cd "$compose_dir" && docker compose logs --no-color --timestamps)
    fi
    (cd "$compose_dir" && docker compose down -v --remove-orphans) >/dev/null 2>&1 ||
      echo "docker compose down -v failed in $compose_dir"
  fi
  rm -rf "$state" "$work"
  exit "$status"
}
trap cleanup EXIT

# The fenced blocks after each marker, the fences left out; a marker without a fence after it is an error.
awk -v page="$page" '
  $0 == "<!-- quickstart -->" { marked = 1; next }
  marked && !inblock {
    if ($0 ~ /^```(sh|bash|shell)[[:space:]]*$/) { inblock = 1; blocks++; marked = 0; printf "\n# block %d\n", blocks; next }
    printf "%s: a <!-- quickstart --> marker is not followed by a sh block\n", page > "/dev/stderr"; bad = 1; exit 1
  }
  inblock && /^```[[:space:]]*$/ { inblock = 0; next }
  inblock { print }
  END {
    if (bad) exit 1
    if (inblock) { printf "%s: a quickstart block is not closed\n", page > "/dev/stderr"; exit 1 }
    if (blocks == 0) { printf "%s: no block is marked <!-- quickstart -->\n", page > "/dev/stderr"; exit 1 }
  }
' "$page" >"$state/commands.sh"

release_url='https://raw\.githubusercontent\.com/muster-io/muster/'
if [ -n "$ref" ]; then
  if ! grep -q "$release_url" "$state/commands.sh"; then
    echo "QUICKSTART_REF is set, but no command of the page downloads from raw.githubusercontent.com/muster-io/muster"
    exit 1
  fi
  sed "s|\\($release_url\\)[^/]*/|\\1$ref/|g" "$state/commands.sh" >"$state/commands.ref.sh"
  mv "$state/commands.ref.sh" "$state/commands.sh"
fi

echo "=== image: ${MUSTER_IMAGE:-the release image of the compose file}; download ref: ${ref:-the release tag of the page}"
echo "=== the commands of Quick start, run in an empty directory:"
cat "$state/commands.sh"
echo "==="
(cd "$work" && bash -euo pipefail "$state/commands.sh")

compose_dir=$(find_compose_dir)
if [ -z "$compose_dir" ]; then
  echo "the commands of the page downloaded no docker-compose.yml"
  exit 1
fi
if [ ! -f "$compose_dir/.env" ]; then
  echo "the commands of the page wrote no .env next to docker-compose.yml"
  exit 1
fi
env_value() { sed -n "s/^$1=//p" "$compose_dir/.env" | tail -n 1; }
public_url=$(env_value MUSTER_PUBLIC_URL)
email=$(env_value MUSTER_BOOTSTRAP_ADMIN_EMAIL)
password=$(env_value MUSTER_BOOTSTRAP_ADMIN_PASSWORD)
if [ -z "$public_url" ] || [ -z "$email" ] || [ -z "$password" ]; then
  echo ".env lacks MUSTER_PUBLIC_URL, MUSTER_BOOTSTRAP_ADMIN_EMAIL or MUSTER_BOOTSTRAP_ADMIN_PASSWORD"
  exit 1
fi

# /health/ready from inside the compose network; the HTTP status of the last answer, or nothing without one.
ready_status() {
  (cd "$compose_dir" && docker compose exec -T postgres wget -S -q -T 5 -O /dev/null http://muster:8082/health/ready 2>&1) |
    awk '$1 ~ /^HTTP\// { status = $2 } END { print status }'
}

deadline=$((SECONDS + ready_timeout))
status=""
until [ "$status" = "200" ]; do
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "/health/ready did not answer 200 within ${ready_timeout} s (last answer: ${status:-none})"
    exit 1
  fi
  sleep 2
  status=$(ready_status || true)
done
echo "ready: /health/ready answered 200"

json_string() {
  local s=$1
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  printf '"%s"' "$s"
}

# The password travels on standard input, so that it shows in no process list.
code=$(printf '{"login":%s,"password":%s}' "$(json_string "$email")" "$(json_string "$password")" |
  curl -sS -o "$state/session.json" -w '%{http_code}' -X POST "$public_url/api/v1/sessions" \
    -H 'Content-Type: application/json' --data-binary @-)
if [ "$code" != "201" ]; then
  echo "signing in as $email answered $code, not 201:"
  cat "$state/session.json"
  echo
  exit 1
fi
login=$(json_string "$email")
if ! grep -qF "\"state\":\"active\"" "$state/session.json" || ! grep -qF "\"login\":$login" "$state/session.json" ||
  ! grep -qF '"role":"admin"' "$state/session.json"; then
  echo "the session of $email is not an active Admin session:"
  sed 's/"csrf_token":"[^"]*"/"csrf_token":"[redacted]"/' "$state/session.json"
  echo
  exit 1
fi
echo "signed in as $email"
ok=1
