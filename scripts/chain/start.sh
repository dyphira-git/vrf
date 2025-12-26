#!/bin/sh
set -eu
(set -o pipefail) 2>/dev/null || true

BINARY=${BINARY:-chaind}
MIN_GAS_PRICES=${MIN_GAS_PRICES:-0uchain}
CHAIN_DIR=${CHAIN_DIR:-"$HOME/.chaind"}
TRACE=${TRACE:-true}
START_RETRY=${START_RETRY:-false}
RETRY_BACKOFF=${RETRY_BACKOFF:-2}

if [ ! -f "$CHAIN_DIR/config/genesis.json" ]; then
    echo "genesis not found at $CHAIN_DIR; run scripts/chain/init.sh first" >&2
    exit 1
fi

start_chain() {
    if [ "$TRACE" = "true" ]; then
        "$BINARY" start --home "$CHAIN_DIR" --minimum-gas-prices "$MIN_GAS_PRICES" --trace
    else
        "$BINARY" start --home "$CHAIN_DIR" --minimum-gas-prices "$MIN_GAS_PRICES"
    fi
}

if [ "$START_RETRY" != "true" ]; then
    start_chain
    exit $?
fi

chain_pid=""
tail_pid=""
watch_pid=""
stop_requested=false

trap 'stop_requested=true; [ -n "$chain_pid" ] && kill "$chain_pid" 2>/dev/null || true; [ -n "$tail_pid" ] && kill "$tail_pid" 2>/dev/null || true; exit 0' INT TERM

while :; do
    start_chain

    if [ "$stop_requested" = "true" ]; then
        exit 0
    fi

    tail -n 20 "$log_file" >&2 || true
    echo "chaind exited (status=$status); retrying in ${RETRY_BACKOFF}s..." >&2
    sleep "$RETRY_BACKOFF"
done
