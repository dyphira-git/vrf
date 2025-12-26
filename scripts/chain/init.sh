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

GENESIS_FILE="$CHAIN_DIR/config/genesis.json"
if [ -f "$GENESIS_FILE" ] && [ "$RESET_CHAIN" != "true" ]; then
    echo "Chain deamon already initialized at $CHAIN_DIR"
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
