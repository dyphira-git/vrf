#!/bin/sh
set -eu
(set -o pipefail) 2>/dev/null || true

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

VRF_RUNTIME_DIR=${VRF_RUNTIME_DIR:-/var/run/vrf}
VRF_DRAND_INFO_FILE=${VRF_DRAND_INFO_FILE:-$VRF_RUNTIME_DIR/drand-info.json}

DRAND_BINARY=${DRAND_BINARY:-drand}
DRAND_ID=${DRAND_ID:-default}
DRAND_DATA_DIR=${DRAND_DATA_DIR:-/var/lib/vrf/drand}
DRAND_PUBLIC_ADDR=${DRAND_PUBLIC_ADDR:-127.0.0.1:8081}
DRAND_PRIVATE_ADDR=${DRAND_PRIVATE_ADDR:-127.0.0.1:4444}
DRAND_CONTROL_ADDR=${DRAND_CONTROL_ADDR:-127.0.0.1:8881}
DRAND_SCHEME=${DRAND_SCHEME:-pedersen-bls-chained}

DRAND_DKG_PERIOD_SECONDS=${DRAND_DKG_PERIOD_SECONDS:-3}
DRAND_DKG_GENESIS_DELAY_SECONDS=${DRAND_DKG_GENESIS_DELAY_SECONDS:-5}

if [ -f "$VRF_DRAND_INFO_FILE" ]; then
    echo "✅ drand info already present at $VRF_DRAND_INFO_FILE"
    exit 0
fi

mkdir -p "$VRF_RUNTIME_DIR"
mkdir -p "$DRAND_DATA_DIR"

extract_port() {
    v=$1
    v=${v##*]}
    v=${v##*:}
    echo "$v"
}

CONTROL_PORT=$(extract_port "$DRAND_CONTROL_ADDR")
PRIVATE_PORT=$(extract_port "$DRAND_PRIVATE_ADDR")
JOINER_ADDR="127.0.0.1:${PRIVATE_PORT}"

export DRAND_FOLDER="$DRAND_DATA_DIR"
export DRAND_CONTROL="$CONTROL_PORT"
export DRAND_ID="$DRAND_ID"
export DRAND_SCHEME="$DRAND_SCHEME"

if command -v python3 >/dev/null 2>&1; then
    : # ok
else
    echo "python3 is required for drand bootstrap" >&2
    exit 1
fi

echo "==> bootstrapping local drand (id=$DRAND_ID, folder=$DRAND_DATA_DIR)"

if ! "$DRAND_BINARY" generate-keypair --scheme "$DRAND_SCHEME" "$JOINER_ADDR" >/dev/null 2>&1; then
    "$DRAND_BINARY" generate-keypair --scheme "$DRAND_SCHEME" "$JOINER_ADDR" || true
fi

DRAND_LOG=$(mktemp 2>/dev/null || mktemp -t vrf-drand-log)
"$DRAND_BINARY" start \
    --folder "$DRAND_DATA_DIR" \
    --private-listen "$DRAND_PRIVATE_ADDR" \
    --public-listen "$DRAND_PUBLIC_ADDR" \
    --control "$CONTROL_PORT" \
    >"$DRAND_LOG" 2>&1 &
DRAND_PID=$!

cleanup() {
    if kill -0 "$DRAND_PID" >/dev/null 2>&1; then
        kill "$DRAND_PID" >/dev/null 2>&1 || true
        wait "$DRAND_PID" >/dev/null 2>&1 || true
    fi
}
trap cleanup INT TERM EXIT

python3 - "$CONTROL_PORT" <<'PY'
import socket
import sys
import time

port = int(sys.argv[1])
deadline = time.time() + 20
while time.time() < deadline:
    s = socket.socket()
    s.settimeout(0.5)
    try:
        s.connect(("127.0.0.1", port))
        s.close()
        sys.exit(0)
    except Exception:
        try:
            s.close()
        except Exception:
            pass
        time.sleep(0.25)

print(f"drand control port {port} did not become ready in time", file=sys.stderr)
sys.exit(1)
PY

TMP_OUT=$(mktemp 2>/dev/null || mktemp -t vrf-drand-chain-info)
if "$DRAND_BINARY" show chain-info --id "$DRAND_ID" >"$TMP_OUT" 2>/dev/null; then
    echo "✅ drand chain already initialized"
else
    echo "==> running drand DKG (single-node devnet)"
    PROPOSAL_FILE="$DRAND_DATA_DIR/proposal.toml"
    "$DRAND_BINARY" dkg generate-proposal --id "$DRAND_ID" --joiner "$JOINER_ADDR" --out "$PROPOSAL_FILE" >/dev/null

    "$DRAND_BINARY" dkg init \
        --id "$DRAND_ID" \
        --proposal "$PROPOSAL_FILE" \
        --threshold 1 \
        --scheme "$DRAND_SCHEME" \
        --period "${DRAND_DKG_PERIOD_SECONDS}s" \
        --catchup-period "${DRAND_DKG_PERIOD_SECONDS}s" \
        --genesis-delay "${DRAND_DKG_GENESIS_DELAY_SECONDS}s" \
        >/dev/null

    "$DRAND_BINARY" dkg join --id "$DRAND_ID" >/dev/null
    "$DRAND_BINARY" dkg execute --id "$DRAND_ID" >/dev/null

    "$DRAND_BINARY" show chain-info --id "$DRAND_ID" >"$TMP_OUT"
fi

python3 - "$TMP_OUT" "$VRF_DRAND_INFO_FILE" <<'PY'
import base64
import json
import re
import sys

path = sys.argv[1]
out_path = sys.argv[2]
raw = open(path, "r", encoding="utf-8").read().strip()

data = None
try:
    data = json.loads(raw)
except Exception:
    data = None

chain_hash_hex = ""
public_key_b64 = ""
period_seconds = 0
genesis_unix_sec = 0

if isinstance(data, dict):
    for key in ("hash", "chain_hash", "chainHash", "ChainHash"):
        v = data.get(key)
        if isinstance(v, str) and v.strip():
            chain_hash_hex = v.strip()
            break
    for key in ("public_key", "publicKey", "PublicKey"):
        v = data.get(key)
        if isinstance(v, str) and v.strip():
            public_key_b64 = v.strip()
            break
    for key in ("period", "period_seconds", "periodSeconds", "PeriodSeconds"):
        v = data.get(key)
        if isinstance(v, (int, float)):
            period_seconds = int(v)
            break
        if isinstance(v, str) and v.strip().isdigit():
            period_seconds = int(v.strip())
            break
    for key in ("genesis_time", "genesis_unix_sec", "genesisUnixSec", "GenesisUnixSec"):
        v = data.get(key)
        if isinstance(v, (int, float)):
            genesis_unix_sec = int(v)
            break
        if isinstance(v, str) and v.strip().lstrip("-").isdigit():
            genesis_unix_sec = int(v.strip())
            break

if not chain_hash_hex:
    m = re.search(r"(?im)^(?:chain\\s*hash|hash)\\s*[:=]\\s*([0-9a-fA-F]{16,})\\s*$", raw)
    if m:
        chain_hash_hex = m.group(1).strip()

if not public_key_b64:
    m = re.search(r"(?im)^(?:public\\s*key|public_key)\\s*[:=]\\s*([A-Za-z0-9+/=]{16,})\\s*$", raw)
    if m:
        public_key_b64 = m.group(1).strip()

if not period_seconds:
    m = re.search(r"(?im)^period\\s*[:=]\\s*(\\d+)\\s*(?:s|sec|seconds)?\\s*$", raw)
    if m:
        period_seconds = int(m.group(1))

if not genesis_unix_sec:
    m = re.search(r"(?im)^(?:genesis\\s*time|genesis_unix_sec|genesis)\\s*[:=]\\s*(-?\\d+)\\s*$", raw)
    if m:
        genesis_unix_sec = int(m.group(1))

if not chain_hash_hex or not public_key_b64 or not period_seconds or not genesis_unix_sec:
    raise SystemExit(f"failed to parse drand chain-info output: {raw[:200]}...")

chain_hash_hex = chain_hash_hex.lower().removeprefix("0x")
chain_hash_bytes = bytes.fromhex(chain_hash_hex)

out = {
    "chain_hash_hex": chain_hash_hex,
    "params": {
        "chain_hash": base64.b64encode(chain_hash_bytes).decode("ascii"),
        "public_key": public_key_b64,
        "period_seconds": int(period_seconds),
        "genesis_unix_sec": int(genesis_unix_sec),
    },
}

with open(out_path, "w", encoding="utf-8") as f:
    json.dump(out, f, indent=2, sort_keys=True)
    f.write("\\n")
PY

echo "✅ wrote drand info to $VRF_DRAND_INFO_FILE"
