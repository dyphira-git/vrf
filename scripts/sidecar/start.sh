#!/bin/sh
set -eu
(set -o pipefail) 2>/dev/null || true

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

VRF_HOME=${VRF_HOME:-}
if [ -z "$VRF_HOME" ]; then
    if [ -n "${HOME:-}" ]; then
        VRF_HOME="$HOME/.vrf"
    else
        VRF_HOME="$(pwd)/.vrf"
    fi
fi

SIDECAR_LISTEN_ADDR=${SIDECAR_LISTEN_ADDR:-127.0.0.1:8090}
DRAND_DATA_DIR=${DRAND_DATA_DIR:-$VRF_HOME/drand}

VRF_HOME="$VRF_HOME" \
    SIDECAR_LISTEN_ADDR="$SIDECAR_LISTEN_ADDR" \
    DRAND_DATA_DIR="$DRAND_DATA_DIR" \
    sh "$SCRIPT_DIR/init.sh"

SIDECAR_BINARY=${SIDECAR_BINARY:-sidecar}
VRF_ALLOW_PUBLIC_BIND=${VRF_ALLOW_PUBLIC_BIND:-false}

METRICS_ENABLED=${METRICS_ENABLED:-false}
METRICS_ADDR=${METRICS_ADDR:-127.0.0.1:8091}
METRICS_CHAIN_ID=${METRICS_CHAIN_ID:-}

DRAND_SUPERVISE=${DRAND_SUPERVISE:-true}
DRAND_HTTP=${DRAND_HTTP:-}
DRAND_PUBLIC_ADDR=${DRAND_PUBLIC_ADDR:-127.0.0.1:8081}
DRAND_PRIVATE_ADDR=${DRAND_PRIVATE_ADDR:-0.0.0.0:4444}
DRAND_CONTROL_ADDR=${DRAND_CONTROL_ADDR:-127.0.0.1:8881}

DRAND_BINARY=${DRAND_BINARY:-drand}
DRAND_VERSION_CHECK=${DRAND_VERSION_CHECK:-strict}

DRAND_CHAIN_HASH=${DRAND_CHAIN_HASH:-}
DRAND_PUBLIC_KEY=${DRAND_PUBLIC_KEY:-}
DRAND_PERIOD_SECONDS=${DRAND_PERIOD_SECONDS:-}
DRAND_GENESIS_UNIX=${DRAND_GENESIS_UNIX:-}

# Optional chain watcher (PLAN §1.8):
# - When CHAIN_GRPC_ADDR is set, the sidecar fetches x/vrf params at startup and
#   watches enabled/reshare_epoch, treating chain params as the source of truth.
# - DRAND_* params become optional in this mode.
CHAIN_GRPC_ADDR=${CHAIN_GRPC_ADDR:-}
CHAIN_RPC_ADDR=${CHAIN_RPC_ADDR:-}
CHAIN_POLL_INTERVAL=${CHAIN_POLL_INTERVAL:-}
CHAIN_WS_ENABLED=${CHAIN_WS_ENABLED:-false}

# Reshare listener (PLAN §1.8 / PRD §5.5):
RESHARE_ENABLED=${RESHARE_ENABLED:-true}
DRAND_RESHARE_ARGS=${DRAND_RESHARE_ARGS:-}
DRAND_RESHARE_TIMEOUT=${DRAND_RESHARE_TIMEOUT:-}

CHAIN_BINARY=${CHAIN_BINARY:-chaind}
CHAIN_DIR=${CHAIN_DIR:-}
if [ -z "$CHAIN_DIR" ]; then
    if [ -f "$VRF_HOME/.chaind/config/genesis.json" ]; then
        CHAIN_DIR="$VRF_HOME/.chaind"
    elif [ -n "${HOME:-}" ]; then
        CHAIN_DIR="$HOME/.chaind"
    else
        CHAIN_DIR="$(pwd)/.chaind"
    fi
fi
GENESIS_FILE=${GENESIS_FILE:-$CHAIN_DIR/config/genesis.json}

PYTHON_BIN=${PYTHON:-}
if [ -z "$PYTHON_BIN" ]; then
    if command -v python3 >/dev/null 2>&1; then
        PYTHON_BIN=python3
    elif command -v python >/dev/null 2>&1; then
        PYTHON_BIN=python
    fi
fi

load_drand_params_from_json() {
    if [ -z "$PYTHON_BIN" ]; then
        return 1
    fi

    "$PYTHON_BIN" - "$1" <<'PY'
import base64
import json
import sys

path = sys.argv[1]
with open(path, "r", encoding="utf-8") as f:
    data = json.load(f)

# Accept either:
# - chain genesis: app_state.vrf.params
# - query response: params
params = (
    (data.get("app_state") or {}).get("vrf") or {}
).get("params") or (data.get("params") or {})

chain_hash_b64 = (params.get("chain_hash") or "").strip()
public_key_b64 = (params.get("public_key") or "").strip()

def to_int(value):
    if value is None:
        return 0
    if isinstance(value, bool):
        return 0
    if isinstance(value, (int, float)):
        return int(value)
    s = str(value).strip()
    if s == "":
        return 0
    try:
        return int(s, 10)
    except ValueError:
        return 0

period_seconds = to_int(params.get("period_seconds"))
genesis_unix = to_int(params.get("genesis_unix_sec"))

chain_hash_hex = ""
if chain_hash_b64:
    try:
        chain_hash_hex = base64.b64decode(chain_hash_b64).hex()
    except Exception:
        chain_hash_hex = ""

print(chain_hash_hex)
print(public_key_b64)
print(period_seconds)
print(genesis_unix)
PY
}

try_load_from_genesis() {
    if [ ! -f "$GENESIS_FILE" ]; then
        return 1
    fi

    GEN_CHAIN_HASH=
    GEN_PUBLIC_KEY=
    GEN_PERIOD_SECONDS=
    GEN_GENESIS_UNIX=
    {
        read -r GEN_CHAIN_HASH || true
        read -r GEN_PUBLIC_KEY || true
        read -r GEN_PERIOD_SECONDS || true
        read -r GEN_GENESIS_UNIX || true
    } <<EOF
$(load_drand_params_from_json "$GENESIS_FILE" 2>/dev/null || true)
EOF

    if [ -z "$DRAND_CHAIN_HASH" ] && [ -n "$GEN_CHAIN_HASH" ]; then
        DRAND_CHAIN_HASH="$GEN_CHAIN_HASH"
    fi
    if [ -z "$DRAND_PUBLIC_KEY" ] && [ -n "$GEN_PUBLIC_KEY" ]; then
        DRAND_PUBLIC_KEY="$GEN_PUBLIC_KEY"
    fi
    if [ -z "$DRAND_PERIOD_SECONDS" ] && [ -n "$GEN_PERIOD_SECONDS" ] && [ "$GEN_PERIOD_SECONDS" -gt 0 ] 2>/dev/null; then
        DRAND_PERIOD_SECONDS="$GEN_PERIOD_SECONDS"
    fi
    if [ -z "$DRAND_GENESIS_UNIX" ] && [ -n "$GEN_GENESIS_UNIX" ] && [ "$GEN_GENESIS_UNIX" -gt 0 ] 2>/dev/null; then
        DRAND_GENESIS_UNIX="$GEN_GENESIS_UNIX"
    fi
}

try_load_from_chain() {
    if ! command -v "$CHAIN_BINARY" >/dev/null 2>&1; then
        return 1
    fi
    if [ ! -d "$CHAIN_DIR" ]; then
        return 1
    fi

    TMP_PARAMS=$(mktemp 2>/dev/null || mktemp -t vrf-sidecar-params)
    if ! "$CHAIN_BINARY" query vrf params --home "$CHAIN_DIR" -o json >"$TMP_PARAMS" 2>/dev/null; then
        rm -f "$TMP_PARAMS" >/dev/null 2>&1 || true
        return 1
    fi

    CHAIN_CHAIN_HASH=
    CHAIN_PUBLIC_KEY=
    CHAIN_PERIOD_SECONDS=
    CHAIN_GENESIS_UNIX=
    {
        read -r CHAIN_CHAIN_HASH || true
        read -r CHAIN_PUBLIC_KEY || true
        read -r CHAIN_PERIOD_SECONDS || true
        read -r CHAIN_GENESIS_UNIX || true
    } <<EOF
$(load_drand_params_from_json "$TMP_PARAMS" 2>/dev/null || true)
EOF
    rm -f "$TMP_PARAMS" >/dev/null 2>&1 || true

    if [ -z "$DRAND_CHAIN_HASH" ] && [ -n "$CHAIN_CHAIN_HASH" ]; then
        DRAND_CHAIN_HASH="$CHAIN_CHAIN_HASH"
    fi
    if [ -z "$DRAND_PUBLIC_KEY" ] && [ -n "$CHAIN_PUBLIC_KEY" ]; then
        DRAND_PUBLIC_KEY="$CHAIN_PUBLIC_KEY"
    fi
    if [ -z "$DRAND_PERIOD_SECONDS" ] && [ -n "$CHAIN_PERIOD_SECONDS" ] && [ "$CHAIN_PERIOD_SECONDS" -gt 0 ] 2>/dev/null; then
        DRAND_PERIOD_SECONDS="$CHAIN_PERIOD_SECONDS"
    fi
    if [ -z "$DRAND_GENESIS_UNIX" ] && [ -n "$CHAIN_GENESIS_UNIX" ] && [ "$CHAIN_GENESIS_UNIX" -gt 0 ] 2>/dev/null; then
        DRAND_GENESIS_UNIX="$CHAIN_GENESIS_UNIX"
    fi
}

if [ -n "$CHAIN_GRPC_ADDR" ]; then
    # Chain watcher enabled; skip DRAND_* autodetection and missing checks.
    :
else
MISSING=""
if [ -z "$DRAND_CHAIN_HASH" ]; then
    MISSING="$MISSING DRAND_CHAIN_HASH"
fi
if [ -z "$DRAND_PUBLIC_KEY" ]; then
    MISSING="$MISSING DRAND_PUBLIC_KEY"
fi
if [ -z "$DRAND_PERIOD_SECONDS" ]; then
    MISSING="$MISSING DRAND_PERIOD_SECONDS"
fi
if [ -z "$DRAND_GENESIS_UNIX" ]; then
    MISSING="$MISSING DRAND_GENESIS_UNIX"
fi

if [ -n "$MISSING" ]; then
    # Best-effort auto-wiring: prefer genesis (fast/local), then chain query.
    try_load_from_genesis || true

    MISSING=""
    if [ -z "$DRAND_CHAIN_HASH" ]; then
        MISSING="$MISSING DRAND_CHAIN_HASH"
    fi
    if [ -z "$DRAND_PUBLIC_KEY" ]; then
        MISSING="$MISSING DRAND_PUBLIC_KEY"
    fi
    if [ -z "$DRAND_PERIOD_SECONDS" ]; then
        MISSING="$MISSING DRAND_PERIOD_SECONDS"
    fi
    if [ -z "$DRAND_GENESIS_UNIX" ]; then
        MISSING="$MISSING DRAND_GENESIS_UNIX"
    fi

    if [ -n "$MISSING" ]; then
        try_load_from_chain || true
    fi
fi

MISSING=""
if [ -z "$DRAND_CHAIN_HASH" ]; then
    MISSING="$MISSING DRAND_CHAIN_HASH"
fi
if [ -z "$DRAND_PUBLIC_KEY" ]; then
    MISSING="$MISSING DRAND_PUBLIC_KEY"
fi
if [ -z "$DRAND_PERIOD_SECONDS" ]; then
    MISSING="$MISSING DRAND_PERIOD_SECONDS"
fi
if [ -z "$DRAND_GENESIS_UNIX" ]; then
    MISSING="$MISSING DRAND_GENESIS_UNIX"
fi
if [ -n "$MISSING" ]; then
    echo "Missing required environment variables:$MISSING" >&2
    echo "Tried to load VRF params from:" >&2
    echo "  - GENESIS_FILE=$GENESIS_FILE" >&2
    echo "  - $CHAIN_BINARY query vrf params --home $CHAIN_DIR" >&2
    if [ -z "$PYTHON_BIN" ]; then
        echo "  - Note: python3/python not found; auto-wiring from genesis/chain is disabled." >&2
    fi
    echo "Provide DRAND_* env vars explicitly, or ensure x/vrf params are set (chain_hash/public_key/genesis_unix_sec/period_seconds)." >&2
    exit 1
fi
fi

set -- \
    --listen-addr "$SIDECAR_LISTEN_ADDR" \
    --vrf-allow-public-bind="$VRF_ALLOW_PUBLIC_BIND" \
    --metrics-enabled="$METRICS_ENABLED" \
    --metrics-addr "$METRICS_ADDR" \
    --drand-supervise="$DRAND_SUPERVISE" \
    --drand-binary "$DRAND_BINARY" \
    --drand-data-dir "$DRAND_DATA_DIR" \
    --drand-public-addr "$DRAND_PUBLIC_ADDR" \
    --drand-private-addr "$DRAND_PRIVATE_ADDR" \
    --drand-control-addr "$DRAND_CONTROL_ADDR" \
    --drand-chain-hash "$DRAND_CHAIN_HASH" \
    --drand-public-key "$DRAND_PUBLIC_KEY" \
    --drand-period-seconds "$DRAND_PERIOD_SECONDS" \
    --drand-genesis-unix "$DRAND_GENESIS_UNIX"

if [ -n "$METRICS_CHAIN_ID" ]; then
    set -- "$@" --chain-id "$METRICS_CHAIN_ID"
fi
if [ -n "$DRAND_HTTP" ]; then
    set -- "$@" --drand-http "$DRAND_HTTP"
fi
set -- "$@" --drand-version-check "$DRAND_VERSION_CHECK"

if [ -n "$CHAIN_GRPC_ADDR" ]; then
    set -- "$@" --chain-grpc-addr "$CHAIN_GRPC_ADDR"
fi
if [ -n "$CHAIN_RPC_ADDR" ]; then
    set -- "$@" --chain-rpc-addr "$CHAIN_RPC_ADDR"
fi
if [ -n "$CHAIN_POLL_INTERVAL" ]; then
    set -- "$@" --chain-poll-interval "$CHAIN_POLL_INTERVAL"
fi
set -- "$@" --chain-ws-enabled="$CHAIN_WS_ENABLED"

set -- "$@" --reshare-enabled="$RESHARE_ENABLED"
if [ -n "$DRAND_RESHARE_TIMEOUT" ]; then
    set -- "$@" --drand-reshare-timeout "$DRAND_RESHARE_TIMEOUT"
fi
if [ -n "$DRAND_RESHARE_ARGS" ]; then
    for arg in $DRAND_RESHARE_ARGS; do
        set -- "$@" --drand-reshare-arg "$arg"
    done
fi

exec "$SIDECAR_BINARY" "$@"
