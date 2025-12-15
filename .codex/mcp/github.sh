#!/usr/bin/env bash
set -euo pipefail

container_name="vexxvakan-github"
legacy_container_name="codex-vrf-github"
image="ghcr.io/github/github-mcp-server"

if [[ -z "${GITHUB_PERSONAL_ACCESS_TOKEN:-}" && -n "${GITHUB_TOKEN:-}" ]]; then
  export GITHUB_PERSONAL_ACCESS_TOKEN="$GITHUB_TOKEN"
fi

create_container() {
  if [[ -z "${GITHUB_PERSONAL_ACCESS_TOKEN:-}" ]]; then
    echo "GITHUB_PERSONAL_ACCESS_TOKEN is not set in this process environment (it must be exported before launching Codex)." >&2
    exit 1
  fi

  docker create \
    -i \
    --name "$container_name" \
    -e GITHUB_PERSONAL_ACCESS_TOKEN \
    -e GITHUB_TOOLSETS \
    "$image" \
    stdio \
    >/dev/null
}

# Create a long-lived container once, then re-use it across Codex sessions.
# Each MCP session runs its own server process via `docker exec`, which allows
# multiple concurrent sessions.
if ! docker container inspect "$container_name" >/dev/null 2>&1; then
  if docker container inspect "$legacy_container_name" >/dev/null 2>&1; then
    docker rename "$legacy_container_name" "$container_name" >/dev/null
  fi
fi

if ! docker container inspect "$container_name" >/dev/null 2>&1; then
  create_container
fi

is_running="$(docker inspect -f '{{.State.Running}}' "$container_name" 2>/dev/null || echo false)"
if [[ "$is_running" != "true" ]]; then
  docker start "$container_name" >/dev/null || true

  is_running="$(docker inspect -f '{{.State.Running}}' "$container_name" 2>/dev/null || echo false)"
  if [[ "$is_running" != "true" ]]; then
    docker rm "$container_name" >/dev/null 2>&1 || true
    create_container
    docker start "$container_name" >/dev/null
  fi
fi

if [[ -z "${GITHUB_PERSONAL_ACCESS_TOKEN:-}" ]]; then
  echo "GITHUB_PERSONAL_ACCESS_TOKEN is not set in this process environment (it must be exported before launching Codex)." >&2
  exit 1
fi

exec docker exec \
  -i \
  -e GITHUB_PERSONAL_ACCESS_TOKEN \
  -e GITHUB_TOOLSETS \
  "$container_name" \
  /server/github-mcp-server stdio
