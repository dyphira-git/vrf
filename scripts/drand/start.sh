#!/bin/sh
set -eu
(set -o pipefail) 2>/dev/null || true

DRAND_BINARY=${DRAND_BINARY:-/bin/drand}
DRAND_ID=${DRAND_ID:-default}
DRAND_SCHEME=${DRAND_SCHEME:-pedersen-bls-chained}

DRAND_ROLE=${DRAND_ROLE:-}
DRAND_DATA_DIR=${DRAND_DATA_DIR:-/var/lib/vrf/drand}
DRAND_PRIVATE_ADDR=${DRAND_PRIVATE_ADDR:-0.0.0.0:4444}
DRAND_PUBLIC_ADDR=${DRAND_PUBLIC_ADDR:-0.0.0.0:8081}
DRAND_CONTROL_PORT=${DRAND_CONTROL_PORT:-8881}

# Address other nodes use to reach this node's private API (used for keypair + DKG proposal).
DRAND_KEYPAIR_ADDR=${DRAND_KEYPAIR_ADDR:-}

# Leader only: second joiner address to include in the initial DKG.
DRAND_JOINER_ADDR=${DRAND_JOINER_ADDR:-}

DRAND_DKG_THRESHOLD=${DRAND_DKG_THRESHOLD:-2}
DRAND_DKG_PERIOD_SECONDS=${DRAND_DKG_PERIOD_SECONDS:-3}
DRAND_DKG_GENESIS_DELAY_SECONDS=${DRAND_DKG_GENESIS_DELAY_SECONDS:-5}

VRF_RUNTIME_DIR=${VRF_RUNTIME_DIR:-/var/run/vrf}
VRF_DRAND_INFO_FILE=${VRF_DRAND_INFO_FILE:-$VRF_RUNTIME_DIR/drand-info.json}

if [ -z "$DRAND_ROLE" ]; then
    echo "DRAND_ROLE must be set to 'leader' or 'joiner'" >&2
    exit 1
fi

if [ -z "$DRAND_KEYPAIR_ADDR" ]; then
    echo "DRAND_KEYPAIR_ADDR must be set (e.g. drand1:4444)" >&2
    exit 1
fi

KEY_PATH="$DRAND_DATA_DIR/multibeacon/$DRAND_ID/key/drand_id.public"
if [ ! -f "$KEY_PATH" ]; then
    mkdir -p "$DRAND_DATA_DIR"
    echo "==> generating drand keypair ($DRAND_KEYPAIR_ADDR)"
    "$DRAND_BINARY" generate-keypair \
        --folder "$DRAND_DATA_DIR" \
        --id "$DRAND_ID" \
        --scheme "$DRAND_SCHEME" \
        "$DRAND_KEYPAIR_ADDR" >/dev/null
fi

echo "==> starting drand daemon (id=$DRAND_ID folder=$DRAND_DATA_DIR)"
"$DRAND_BINARY" start \
    --folder "$DRAND_DATA_DIR" \
    --id "$DRAND_ID" \
    --private-listen "$DRAND_PRIVATE_ADDR" \
    --public-listen "$DRAND_PUBLIC_ADDR" \
    --control "$DRAND_CONTROL_PORT" &
DRAND_PID=$!

cleanup() {
    if kill -0 "$DRAND_PID" >/dev/null 2>&1; then
        kill "$DRAND_PID" >/dev/null 2>&1 || true
        wait "$DRAND_PID" >/dev/null 2>&1 || true
    fi
}
trap cleanup INT TERM EXIT

python3 - "$DRAND_CONTROL_PORT" <<'PY'
import socket
import sys
import time

port = int(sys.argv[1])
deadline = time.time() + 30
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

chain_info_ready() {
    "$DRAND_BINARY" show chain-info --id "$DRAND_ID" --control "$DRAND_CONTROL_PORT" >/tmp/chain-info.json 2>/tmp/chain-info.err
}

write_info_file() {
    python3 - /tmp/chain-info.json "$VRF_DRAND_INFO_FILE" <<'PY'
import base64
import json
import os
import sys

chain_info_path = sys.argv[1]
out_path = sys.argv[2]

info = json.load(open(chain_info_path, "r", encoding="utf-8"))

chain_hash_hex = (info.get("hash") or "").strip().lower().removeprefix("0x")
public_key_b64 = (info.get("public_key") or "").strip()
period_seconds = int(info.get("period") or 0)
genesis_unix_sec = int(info.get("genesis_time") or 0)

if not chain_hash_hex or not public_key_b64 or period_seconds <= 0 or genesis_unix_sec <= 0:
    raise SystemExit(f"incomplete chain-info: {info!r}")

chain_hash_bytes = bytes.fromhex(chain_hash_hex)

out = {
    "chain_hash_hex": chain_hash_hex,
    "params": {
        "chain_hash": base64.b64encode(chain_hash_bytes).decode("ascii"),
        "public_key": public_key_b64,
        "period_seconds": period_seconds,
        "genesis_unix_sec": genesis_unix_sec,
    },
}

os.makedirs(os.path.dirname(out_path) or ".", exist_ok=True)
with open(out_path, "w", encoding="utf-8") as f:
    json.dump(out, f, indent=2, sort_keys=True)
    f.write("\\n")
PY
}

if chain_info_ready; then
    if [ "$DRAND_ROLE" = "leader" ] && [ ! -f "$VRF_DRAND_INFO_FILE" ]; then
        mkdir -p "$VRF_RUNTIME_DIR"
        write_info_file
        echo "✅ wrote drand info to $VRF_DRAND_INFO_FILE"
    fi
else
    if [ "$DRAND_ROLE" = "joiner" ]; then
        echo "==> waiting for DKG proposal; attempting join..."
        i=0
        while [ "$i" -lt 240 ]; do
            i=$((i + 1))
            if chain_info_ready; then
                echo "✅ drand chain already initialized"
                break
            fi
            if "$DRAND_BINARY" dkg join --id "$DRAND_ID" --control "$DRAND_CONTROL_PORT" >/dev/null 2>&1; then
                echo "✅ joined DKG"
                break
            fi
            sleep 0.5
        done
    fi

    if [ "$DRAND_ROLE" = "leader" ]; then
        if [ -z "$DRAND_JOINER_ADDR" ]; then
            echo "DRAND_JOINER_ADDR must be set for leader (e.g. drand2:4444)" >&2
            exit 1
        fi

        echo "==> running drand DKG (2-node devnet)"
        PROPOSAL_FILE=$(mktemp 2>/dev/null || mktemp -t vrf-drand-proposal)
        "$DRAND_BINARY" dkg generate-proposal \
            --id "$DRAND_ID" \
            --control "$DRAND_CONTROL_PORT" \
            --joiner "$DRAND_KEYPAIR_ADDR" \
            --joiner "$DRAND_JOINER_ADDR" \
            --out "$PROPOSAL_FILE" >/dev/null

        "$DRAND_BINARY" dkg init \
            --id "$DRAND_ID" \
            --control "$DRAND_CONTROL_PORT" \
            --proposal "$PROPOSAL_FILE" \
            --threshold "$DRAND_DKG_THRESHOLD" \
            --scheme "$DRAND_SCHEME" \
            --period "${DRAND_DKG_PERIOD_SECONDS}s" \
            --catchup-period "${DRAND_DKG_PERIOD_SECONDS}s" \
            --genesis-delay "${DRAND_DKG_GENESIS_DELAY_SECONDS}s" \
            >/dev/null

        "$DRAND_BINARY" dkg execute --id "$DRAND_ID" --control "$DRAND_CONTROL_PORT" >/dev/null

        echo "==> waiting for drand chain-info..."
        i=0
        while [ "$i" -lt 240 ]; do
            i=$((i + 1))
            if chain_info_ready; then
                mkdir -p "$VRF_RUNTIME_DIR"
                write_info_file
                echo "✅ wrote drand info to $VRF_DRAND_INFO_FILE"
                break
            fi
            sleep 0.5
        done

        if [ ! -f "$VRF_DRAND_INFO_FILE" ]; then
            echo "Timed out waiting for drand chain-info after DKG" >&2
            exit 1
        fi
    fi
fi

wait "$DRAND_PID"
