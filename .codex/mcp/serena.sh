#!/usr/bin/env bash
set -euo pipefail

container_name="vexxvakan-serena"
legacy_container_name="codex-vrf-serena"
image="ghcr.io/oraios/serena"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

create_container() {
  docker create \
    -i \
    --name "$container_name" \
    -v "$repo_root:$repo_root" \
    "$image" \
    sleep 2147483647 \
    >/dev/null
}

# Create a long-lived container once, then re-use it across Codex sessions.
# Each MCP session runs its own `serena-mcp-server` process via `docker exec`,
# which allows multiple concurrent sessions.
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

exec docker exec \
  -i \
  -w "$repo_root" \
  -e "SERENA_DOCKER=${SERENA_DOCKER:-1}" \
  -e GITHUB_PERSONAL_ACCESS_TOKEN \
  -e GITHUB_TOOLSETS \
  "$container_name" \
  bash -lc "source /workspaces/serena/.venv/bin/activate && serena-mcp-server --context codex --trace-lsp-communication TRUE --project \"$repo_root\""
