#!/bin/sh
set -eu
(set -o pipefail) 2>/dev/null || true

BINARY=${BINARY:-chaind}
BINARY_NAME=$(basename "$BINARY")

DENOM=${DENOM:-uchain}
CHAIN_ID=${CHAIN_ID:-chain-1}
CHAIN_DIR=${CHAIN_DIR:-"$HOME/.chaind"}
MONIKER=${MONIKER:-node001}
TIMEOUT_COMMIT=${TIMEOUT_COMMIT:-2s}
BLOCK_GAS_LIMIT=${BLOCK_GAS_LIMIT:-10000000}
KEYNAME=${KEYNAME:-validator}
KEYRING_BACKEND=${KEYRING_BACKEND:-test}

RESET_CHAIN=${RESET_CHAIN:-${RESET:-false}}

VRF_RUNTIME_DIR=${VRF_RUNTIME_DIR:-/var/run/vrf}
VRF_DRAND_INFO_FILE=${VRF_DRAND_INFO_FILE:-$VRF_RUNTIME_DIR/drand-info.json}
VRF_ENABLED=${VRF_ENABLED:-true}

GENESIS_FILE="$CHAIN_DIR/config/genesis.json"
if [ -f "$GENESIS_FILE" ] && [ "$RESET_CHAIN" != "true" ]; then
    echo "✅ Chain already initialized at $CHAIN_DIR"
    exit 0
fi

if [ "$RESET_CHAIN" = "true" ]; then
    if command -v pgrep >/dev/null 2>&1 && pgrep -x "$BINARY_NAME" >/dev/null 2>&1; then
        echo "Terminating previous $BINARY_NAME..."
        if command -v killall >/dev/null 2>&1; then
            killall "$BINARY_NAME" >/dev/null 2>&1 || true
        else
            pkill -x "$BINARY_NAME" >/dev/null 2>&1 || true
        fi
    fi
    rm -rf "$CHAIN_DIR" >/dev/null 2>&1 || true
fi
mkdir -p "$CHAIN_DIR"

$BINARY init "$MONIKER" --home "$CHAIN_DIR" --chain-id "$CHAIN_ID" --default-denom "$DENOM" --overwrite

sed_inplace() {
    if [ "$(uname)" = "Darwin" ]; then
        sed -i '' "$@"
    else
        sed -i "$@"
    fi
}
sed_inplace 's/"time_iota_ms": "1000"/"time_iota_ms": "10"/' "$GENESIS_FILE"
sed_inplace 's/"max_gas": "-1"/"max_gas": "'"$BLOCK_GAS_LIMIT"'"/' "$GENESIS_FILE"

$BINARY config set app api.enable true --home "$CHAIN_DIR"
$BINARY config set app api.address "tcp://0.0.0.0:1317" --home "$CHAIN_DIR"
$BINARY config set app api.swagger false --home "$CHAIN_DIR"
$BINARY config set app api.enabled-unsafe-cors true --home "$CHAIN_DIR"
$BINARY config set app grpc.address "0.0.0.0:9090" --home "$CHAIN_DIR"

$BINARY config set config rpc.laddr "tcp://0.0.0.0:26657" --home "$CHAIN_DIR" --skip-validate
$BINARY config set config rpc.cors_allowed_origins "*" --home "$CHAIN_DIR" --skip-validate
$BINARY config set config consensus.timeout_commit "$TIMEOUT_COMMIT" --home "$CHAIN_DIR" --skip-validate

$BINARY keys add "$KEYNAME" --home "$CHAIN_DIR" --keyring-backend "$KEYRING_BACKEND" >/dev/null 2>&1 || true
$BINARY genesis add-genesis-account "$KEYNAME" "1000000000$DENOM" --home "$CHAIN_DIR" --keyring-backend "$KEYRING_BACKEND"

for addr in "$@"; do
  echo "$addr"
  $BINARY genesis add-genesis-account "$addr" "1000000000$DENOM" --home "$CHAIN_DIR" >/dev/null 2>&1 || true
done

$BINARY genesis gentx "$KEYNAME" "250000000$DENOM" --home "$CHAIN_DIR" --chain-id "$CHAIN_ID" --keyring-backend "$KEYRING_BACKEND"
$BINARY genesis collect-gentxs --home "$CHAIN_DIR" >/dev/null 2>&1

if [ "$VRF_ENABLED" = "true" ]; then
    if ! command -v jq >/dev/null 2>&1; then
        echo "jq is required to patch x/vrf params in genesis (install jq or set VRF_ENABLED=false)" >&2
        exit 1
    fi

    if [ ! -f "$VRF_DRAND_INFO_FILE" ]; then
        echo "Waiting for drand info file ($VRF_DRAND_INFO_FILE) ..."
        i=0
        while [ "$i" -lt 240 ] && [ ! -f "$VRF_DRAND_INFO_FILE" ]; do
            i=$((i + 1))
            sleep 0.5
        done
    fi

    if [ ! -f "$VRF_DRAND_INFO_FILE" ]; then
        echo "Timed out waiting for VRF_DRAND_INFO_FILE=$VRF_DRAND_INFO_FILE" >&2
        echo "Ensure the sidecar is running and DRAND_BOOTSTRAP=true, or provide DRAND_* env vars and generate the file." >&2
        exit 1
    fi

    CHAIN_HASH_B64=$(jq -r '.params.chain_hash // empty' "$VRF_DRAND_INFO_FILE")
    PUBLIC_KEY_B64=$(jq -r '.params.public_key // empty' "$VRF_DRAND_INFO_FILE")
    PERIOD_SECONDS=$(jq -r '.params.period_seconds // empty' "$VRF_DRAND_INFO_FILE")
    GENESIS_UNIX_SEC=$(jq -r '.params.genesis_unix_sec // empty' "$VRF_DRAND_INFO_FILE")

    if [ -z "$CHAIN_HASH_B64" ] || [ -z "$PUBLIC_KEY_B64" ] || [ -z "$PERIOD_SECONDS" ] || [ -z "$GENESIS_UNIX_SEC" ]; then
        echo "Invalid drand info file (missing required fields): $VRF_DRAND_INFO_FILE" >&2
        exit 1
    fi

    TMP_GENESIS=$(mktemp 2>/dev/null || mktemp -t vrf-genesis)
    jq \
        --arg chain_hash "$CHAIN_HASH_B64" \
        --arg public_key "$PUBLIC_KEY_B64" \
        --argjson period_seconds "$PERIOD_SECONDS" \
        --argjson genesis_unix_sec "$GENESIS_UNIX_SEC" \
        '
        .app_state.vrf.params.chain_hash = $chain_hash
        | .app_state.vrf.params.public_key = $public_key
        | .app_state.vrf.params.period_seconds = $period_seconds
        | .app_state.vrf.params.genesis_unix_sec = $genesis_unix_sec
        | .app_state.vrf.params.enabled = true
        ' \
        "$GENESIS_FILE" >"$TMP_GENESIS"
    mv "$TMP_GENESIS" "$GENESIS_FILE"

    echo "✅ Patched x/vrf params from $VRF_DRAND_INFO_FILE"
fi
