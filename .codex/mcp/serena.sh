#!/usr/bin/env bash

# If invoked as `sh ./serena.sh`, re-exec under bash to avoid subtle POSIX shell incompatibilities.
if [ -z "${BASH_VERSION:-}" ]; then
  exec bash "$0" "$@"
fi

set -euo pipefail

compose_file="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/serena.compose.yaml"
service_name="serena"
container_name="${SERENA_CONTAINER_NAME:-serena}"

if docker compose version >/dev/null 2>&1; then
  compose_cmd=(docker compose)
  compose_quiet_flags=(--progress quiet --ansi never)
else
  compose_cmd=(docker-compose)
  compose_quiet_flags=()
fi
compose_cmd+=(-f "$compose_file")
compose_quiet_cmd=("${compose_cmd[@]}" "${compose_quiet_flags[@]}")

container_health_status() {
  docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$container_name" 2>/dev/null || true
}

container_is_running() {
  docker inspect -f '{{.State.Running}}' "$container_name" 2>/dev/null || echo false
}

container_mounts() {
  docker inspect -f '{{range .Mounts}}{{printf "%s:%s\n" .Source .Destination}}{{end}}' "$container_name" 2>/dev/null || true
}

container_is_healthy_for() {
  local host_pwd="$1"
  local host_config_dir="$2"

  if ! docker container inspect "$container_name" >/dev/null 2>&1; then
    return 1
  fi

  local running
  running="$(container_is_running)"
  if [[ "$running" != "true" ]]; then
    return 1
  fi

  local health
  health="$(container_health_status)"
  if [[ "$health" != "healthy" ]]; then
    return 1
  fi

  local mounts
  mounts="$(container_mounts)"
  grep -Fqx "$host_pwd:$host_pwd" <<<"$mounts" || return 1
  grep -Fqx "$host_config_dir:/workspaces/serena/config" <<<"$mounts" || return 1

  return 0
}

wait_for_container_healthy() {
  local host_pwd="$1"
  local host_config_dir="$2"
  local wait_seconds="${SERENA_WAIT_FOR_HEALTH_SECONDS:-20}"

  if [[ "$wait_seconds" == "0" ]]; then
    return 0
  fi

  local end=$((SECONDS + wait_seconds))
  while [[ "$SECONDS" -lt "$end" ]]; do
    if container_is_healthy_for "$host_pwd" "$host_config_dir"; then
      return 0
    fi
    sleep 1
  done

  return 1
}

usage() {
  cat <<EOF >&2
Usage: $(basename "$0") [up|up-detached|down|logs|stdio]

Commands:
  up            Start Serena via docker compose (foreground)
  up-detached   Start Serena via docker compose (detached)
  down          Stop Serena
  logs          Follow Serena logs
  stdio         Run Serena MCP server over stdio (for Codex)

Default:
  - interactive shell: up
  - non-interactive:   stdio
EOF
}

cmd="${1:-}"
case "$cmd" in
  "" )
    host_pwd="$(pwd -P)"
    host_config_dir="${SERENA_HOST_CONFIG_DIR:-"$host_pwd/.serena"}"

    if [[ -t 0 && -t 1 ]]; then
      exec SERENA_HOST_PWD="$host_pwd" SERENA_HOST_CONFIG_DIR="$host_config_dir" \
        "${compose_cmd[@]}" up --remove-orphans "$service_name"
    fi

    exec SERENA_HOST_PWD="$host_pwd" SERENA_HOST_CONFIG_DIR="$host_config_dir" \
      "${compose_quiet_cmd[@]}" run --rm --no-deps -T \
      -w "$host_pwd" \
      -e "SERENA_DOCKER=${SERENA_DOCKER:-1}" \
      -e GITHUB_PERSONAL_ACCESS_TOKEN \
      -e GITHUB_TOOLSETS \
      "$service_name" \
      serena-mcp-server --context "${SERENA_CONTEXT:-codex}" --trace-lsp-communication "${SERENA_TRACE_LSP_COMMUNICATION:-TRUE}" --project "$host_pwd"
    ;;
  up)
    shift
    host_pwd="$(pwd -P)"
    host_config_dir="${SERENA_HOST_CONFIG_DIR:-"$host_pwd/.serena"}"
    exec SERENA_HOST_PWD="$host_pwd" SERENA_HOST_CONFIG_DIR="$host_config_dir" \
      "${compose_cmd[@]}" up --remove-orphans "$service_name" "$@"
    ;;
  up-detached)
    shift
    host_pwd="$(pwd -P)"
    host_config_dir="${SERENA_HOST_CONFIG_DIR:-"$host_pwd/.serena"}"

    if container_is_healthy_for "$host_pwd" "$host_config_dir"; then
      exit 0
    fi

    SERENA_HOST_PWD="$host_pwd" SERENA_HOST_CONFIG_DIR="$host_config_dir" \
      "${compose_cmd[@]}" up --detach --remove-orphans "$service_name" "$@"
    wait_for_container_healthy "$host_pwd" "$host_config_dir" || true
    SERENA_HOST_PWD="$host_pwd" SERENA_HOST_CONFIG_DIR="$host_config_dir" \
      "${compose_cmd[@]}" ps "$service_name" >&2 || true
    ;;
  down)
    shift
    host_pwd="$(pwd -P)"
    host_config_dir="${SERENA_HOST_CONFIG_DIR:-"$host_pwd/.serena"}"
    exec SERENA_HOST_PWD="$host_pwd" SERENA_HOST_CONFIG_DIR="$host_config_dir" \
      "${compose_cmd[@]}" down "$@"
    ;;
  logs)
    shift
    host_pwd="$(pwd -P)"
    host_config_dir="${SERENA_HOST_CONFIG_DIR:-"$host_pwd/.serena"}"
    exec SERENA_HOST_PWD="$host_pwd" SERENA_HOST_CONFIG_DIR="$host_config_dir" \
      "${compose_cmd[@]}" logs -f "$service_name" "$@"
    ;;
  stdio)
    shift
    host_pwd="$(pwd -P)"
    host_config_dir="${SERENA_HOST_CONFIG_DIR:-"$host_pwd/.serena"}"

    exec SERENA_HOST_PWD="$host_pwd" SERENA_HOST_CONFIG_DIR="$host_config_dir" \
      "${compose_quiet_cmd[@]}" run --rm --no-deps -T \
      -w "$host_pwd" \
      -e "SERENA_DOCKER=${SERENA_DOCKER:-1}" \
      -e GITHUB_PERSONAL_ACCESS_TOKEN \
      -e GITHUB_TOOLSETS \
      "$service_name" \
      serena-mcp-server --context "${SERENA_CONTEXT:-codex}" --trace-lsp-communication "${SERENA_TRACE_LSP_COMMUNICATION:-TRUE}" --project "$host_pwd"
    ;;
  -h|--help|help)
    usage
    exit 0
    ;;
  *)
    usage
    exit 2
    ;;
esac
