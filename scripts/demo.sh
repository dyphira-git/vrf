#!/bin/sh
set -eu
(set -o pipefail) 2>/dev/null || true

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(dirname "$SCRIPT_DIR")

COMPOSE_FILE=${COMPOSE_FILE:-"$ROOT_DIR/contrib/demo.compose.yaml"}
DEMO_DIR=${DEMO_DIR:-"$ROOT_DIR/.demo"}

CHAIN_ID=${CHAIN_ID:-vrf-demo-1}
DENOM=${DENOM:-uchain}
KEYRING_BACKEND=${KEYRING_BACKEND:-test}

CHAIN_IMAGE=${CHAIN_IMAGE:-vrf-chain:local}
SIDECAR_IMAGE=${SIDECAR_IMAGE:-vrf-sidecar:local}

VRF_PERIOD=${VRF_PERIOD:-2s}
VRF_THRESHOLD=${VRF_THRESHOLD:-2}
DRAND_IDENTITY_HOSTS=${DRAND_IDENTITY_HOSTS:-sidecar-1,sidecar-2}
DRAND_WAIT_TIMEOUT=${DRAND_WAIT_TIMEOUT:-60}

TIMEOUT_COMMIT=${TIMEOUT_COMMIT:-2s}
BLOCK_GAS_LIMIT=${BLOCK_GAS_LIMIT:-10000000}
MIN_GAS_PRICES=${MIN_GAS_PRICES:-0uchain}

RESET=${RESET:-false}
SKIP_BUILD=${SKIP_BUILD:-true}

NODE1_HOME="$DEMO_DIR/node1"
NODE2_HOME="$DEMO_DIR/node2"
DRAND_DIR="$DEMO_DIR/drand"

if docker compose version >/dev/null 2>&1; then
  DOCKER_COMPOSE="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
  DOCKER_COMPOSE="docker-compose"
else
  echo "docker compose not found" >&2
  exit 1
fi

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

require_image() {
  image=$1
  if ! docker image inspect "$image" >/dev/null 2>&1; then
    echo "missing docker image: $image" >&2
    echo "build it first (e.g. make docker-build or tag an existing image as $image)" >&2
    exit 1
  fi
}

run_fixture_generator() {
  if command -v go >/dev/null 2>&1; then
    go run ./scripts/demo/fixture \
      --out "$DRAND_DIR" \
      --nodes 2 \
      --period "$VRF_PERIOD" \
      --threshold "$VRF_THRESHOLD" \
      --identity-hosts "$DRAND_IDENTITY_HOSTS"
    return 0
  fi

  echo "go not found; falling back to a Go container (this may take a while)" >&2
  docker run --rm \
    -v "$ROOT_DIR":/repo \
    -w /repo \
    golang:1.25-alpine \
    sh -lc "apk add --no-cache git >/dev/null 2>&1 && go run ./scripts/demo/fixture --out \"$DRAND_DIR\" --nodes 2 --period \"$VRF_PERIOD\" --threshold \"$VRF_THRESHOLD\" --identity-hosts \"$DRAND_IDENTITY_HOSTS\""
}

if [ "$RESET" = "true" ]; then
  $DOCKER_COMPOSE -f "$COMPOSE_FILE" down >/dev/null 2>&1 || true
  rm -rf "$DEMO_DIR"
fi

mkdir -p "$DEMO_DIR"

if [ "$SKIP_BUILD" != "true" ]; then
  echo "SKIP_BUILD=false is not supported for demo; build images separately and re-run" >&2
  exit 1
fi

require_cmd docker
require_image "$CHAIN_IMAGE"
require_image "$SIDECAR_IMAGE"

identity_host1=$(printf '%s' "$DRAND_IDENTITY_HOSTS" | awk -F, '{print $1}' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')
if [ -n "$identity_host1" ] && [ -f "$DRAND_DIR/metadata.json" ]; then
  if ! grep -q "\"identity_addr\": \"${identity_host1}:" "$DRAND_DIR/metadata.json"; then
    rm -rf "$DRAND_DIR"
  fi
fi

if [ ! -f "$DRAND_DIR/metadata.json" ]; then
  run_fixture_generator
fi

compute_vote_extensions_height() {
  python3 - <<'PY'
import json
import math
import os
import time

with open(os.environ["DRAND_META"], "r") as fh:
    meta = json.load(fh)

period = int(meta.get("period_seconds", 0) or 0)
catchup = int(meta.get("catchup_period_seconds", 0) or 0)
safety = int(meta.get("safety_margin_seconds", 0) or 0)
genesis = int(meta.get("genesis_unix_sec", 0) or 0)

def parse_duration(value: str) -> float:
    if not value:
        return 2.0
    v = value.strip().lower()
    try:
        if v.endswith("ms"):
            return float(v[:-2]) / 1000.0
        if v.endswith("s"):
            return float(v[:-1])
        if v.endswith("m"):
            return float(v[:-1]) * 60.0
        if v.endswith("h"):
            return float(v[:-1]) * 3600.0
        return float(v)
    except Exception:
        return 2.0

timeout_commit = parse_duration(os.environ.get("TIMEOUT_COMMIT", "2s"))
if timeout_commit <= 0:
    timeout_commit = 2.0

if period <= 0:
    print(10)
    raise SystemExit

if catchup <= 0:
    catchup = 1

now = int(time.time())
d = max(0, now - genesis)
gap = d - safety + period
if gap < 0:
    gap = 0

if period <= catchup:
    t_catchup = float(gap)
else:
    t_catchup = (catchup * gap) / (period - catchup)

buffer = max(10, safety)
t_total = t_catchup + buffer
height = int(math.ceil(t_total / timeout_commit))
if height < 10:
    height = 10

print(height)
PY
}

VOTE_EXT_HEIGHT=$(DRAND_META="$DRAND_DIR/metadata.json" compute_vote_extensions_height)

if [ ! -f "$NODE1_HOME/config/genesis.json" ] || [ ! -f "$NODE2_HOME/config/genesis.json" ]; then
  rm -rf "$NODE1_HOME" "$NODE2_HOME"
  mkdir -p "$NODE1_HOME" "$NODE2_HOME"

  INIT_SCRIPT=$(cat <<'EOF'
set -eu

NODE1_HOME=/var/cosmos-chain/node1
NODE2_HOME=/var/cosmos-chain/node2
META=/var/cosmos-chain/drand-fixture/metadata.json
VOTE_EXT_HEIGHT=${VOTE_EXT_HEIGHT:-10}

node_init() {
  home=$1
  moniker=$2
  chaind init "$moniker" --home "$home" --chain-id "$CHAIN_ID" --default-denom "$DENOM" --overwrite

  chaind config set app api.enable true --home "$home"
  chaind config set app api.address "tcp://0.0.0.0:1317" --home "$home"
  chaind config set app api.swagger false --home "$home"
  chaind config set app api.enabled-unsafe-cors true --home "$home"
  chaind config set app grpc.address "0.0.0.0:9090" --home "$home"

  chaind config set config rpc.laddr "tcp://0.0.0.0:26657" --home "$home" --skip-validate
  chaind config set config rpc.cors_allowed_origins "*" --home "$home" --skip-validate
  chaind config set config consensus.timeout_commit "$TIMEOUT_COMMIT" --home "$home" --skip-validate
}

node_init "$NODE1_HOME" "node1"
node_init "$NODE2_HOME" "node2"

chaind keys add validator --home "$NODE1_HOME" --keyring-backend "$KEYRING_BACKEND" >/dev/null 2>&1 || true
chaind keys add validator --home "$NODE2_HOME" --keyring-backend "$KEYRING_BACKEND" >/dev/null 2>&1 || true

ADDR1=$(chaind keys show validator -a --home "$NODE1_HOME" --keyring-backend "$KEYRING_BACKEND")
ADDR2=$(chaind keys show validator -a --home "$NODE2_HOME" --keyring-backend "$KEYRING_BACKEND")
VALOPER1=$(chaind keys show validator -a --bech val --home "$NODE1_HOME" --keyring-backend "$KEYRING_BACKEND")
VALOPER2=$(chaind keys show validator -a --bech val --home "$NODE2_HOME" --keyring-backend "$KEYRING_BACKEND")

chaind genesis add-genesis-account "$ADDR1" "1000000000$DENOM" --home "$NODE1_HOME" --keyring-backend "$KEYRING_BACKEND"
chaind genesis add-genesis-account "$ADDR2" "1000000000$DENOM" --home "$NODE1_HOME" --keyring-backend "$KEYRING_BACKEND"
chaind genesis add-genesis-account "$ADDR1" "1000000000$DENOM" --home "$NODE2_HOME" --keyring-backend "$KEYRING_BACKEND"
chaind genesis add-genesis-account "$ADDR2" "1000000000$DENOM" --home "$NODE2_HOME" --keyring-backend "$KEYRING_BACKEND"

chaind genesis gentx validator "250000000$DENOM" --home "$NODE1_HOME" --chain-id "$CHAIN_ID" --keyring-backend "$KEYRING_BACKEND"
chaind genesis gentx validator "250000000$DENOM" --home "$NODE2_HOME" --chain-id "$CHAIN_ID" --keyring-backend "$KEYRING_BACKEND"

cp "$NODE2_HOME/config/gentx/"* "$NODE1_HOME/config/gentx/"
chaind genesis collect-gentxs --home "$NODE1_HOME" >/dev/null 2>&1

CHAIN_HASH_B64=$(jq -r '.chain_hash_b64' "$META")
PUBLIC_KEY_B64=$(jq -r '.public_key_b64' "$META")
PERIOD_SECONDS=$(jq -r '.period_seconds' "$META")
GENESIS_UNIX=$(jq -r '.genesis_unix_sec' "$META")
SAFETY_MARGIN=$(jq -r '.safety_margin_seconds' "$META")
CATCHUP_SECONDS=$(jq -r '.catchup_period_seconds' "$META")
THRESHOLD=$(jq -r '.threshold' "$META")

SHARE1=$(jq -r '.nodes[0].share_pubkey_b64' "$META")
SHARE2=$(jq -r '.nodes[1].share_pubkey_b64' "$META")

DRAND_NODE1=$(jq -r '.nodes[0].data_dir' "$META")
DRAND_NODE2=$(jq -r '.nodes[1].data_dir' "$META")

PUB_ADDR1=$(jq -r '.nodes[0].public_addr' "$META")
PRIV_ADDR1=$(jq -r '.nodes[0].private_addr' "$META")
CTRL_ADDR1=$(jq -r '.nodes[0].control_addr' "$META")

PUB_ADDR2=$(jq -r '.nodes[1].public_addr' "$META")
PRIV_ADDR2=$(jq -r '.nodes[1].private_addr' "$META")
CTRL_ADDR2=$(jq -r '.nodes[1].control_addr' "$META")

GENESIS="$NODE1_HOME/config/genesis.json"
TMP_GENESIS="$GENESIS.tmp"

jq \
  --arg chain_hash "$CHAIN_HASH_B64" \
  --arg public_key "$PUBLIC_KEY_B64" \
  --arg period "$PERIOD_SECONDS" \
  --arg genesis_unix "$GENESIS_UNIX" \
  --arg safety_margin "$SAFETY_MARGIN" \
  --arg block_gas "$BLOCK_GAS_LIMIT" \
  --arg vote_ext_height "$VOTE_EXT_HEIGHT" \
  --arg addr1 "$ADDR1" \
  --arg addr2 "$ADDR2" \
  --arg valoper1 "$VALOPER1" \
  --arg valoper2 "$VALOPER2" \
  --arg share1 "$SHARE1" \
  --arg share2 "$SHARE2" \
  '
  .app_state.vrf //= {} |
  .app_state.vrf.params //= {} |
  .app_state.vrf.params.chain_hash = $chain_hash |
  .app_state.vrf.params.public_key = $public_key |
  .app_state.vrf.params.period_seconds = $period |
  .app_state.vrf.params.genesis_unix_sec = $genesis_unix |
  .app_state.vrf.params.safety_margin_seconds = $safety_margin |
  .app_state.vrf.params.enabled = true |
  .app_state.vrf.params.reshare_epoch = "0" |
  .app_state.vrf.params.slashing_grace_blocks = "0" |
  .app_state.vrf.committee = [
    {address: $addr1, label: "validator-1"},
    {address: $addr2, label: "validator-2"}
  ] |
  .app_state.vrf.identities = [
    {validator_address: $valoper1, drand_bls_public_key: $share1, chain_hash: $chain_hash, signal_unix_sec: "0", signal_reshare_epoch: "0"},
    {validator_address: $valoper2, drand_bls_public_key: $share2, chain_hash: $chain_hash, signal_unix_sec: "0", signal_reshare_epoch: "0"}
  ] |
  (if .consensus_params? then
      .consensus_params.block.time_iota_ms = "10" |
      .consensus_params.block.max_gas = $block_gas |
      .consensus_params.abci.vote_extensions_enable_height = $vote_ext_height
   else . end) |
  (if .consensus? and .consensus.params? then
      .consensus.params.block.time_iota_ms = "10" |
      .consensus.params.block.max_gas = $block_gas |
      .consensus.params.abci.vote_extensions_enable_height = $vote_ext_height
   else . end)
  ' "$GENESIS" > "$TMP_GENESIS"

mv "$TMP_GENESIS" "$GENESIS"
cp "$GENESIS" "$NODE2_HOME/config/genesis.json"

NODE1_ID=$(chaind tendermint show-node-id --home "$NODE1_HOME")
NODE2_ID=$(chaind tendermint show-node-id --home "$NODE2_HOME")

chaind config set config p2p.persistent_peers "$NODE2_ID@chain-2:26656" --home "$NODE1_HOME" --skip-validate
chaind config set config p2p.persistent_peers "$NODE1_ID@chain-1:26656" --home "$NODE2_HOME" --skip-validate

set_client_toml() {
  file=$1
  host=$2
  sed -i "s|^node *=.*|node = \"http://$host:26657\"|" "$file"
  if grep -q '^grpc-addr' "$file"; then
    sed -i "s|^grpc-addr *=.*|grpc-addr = \"$host:9090\"|" "$file"
  else
    printf '\n# gRPC endpoint\ngrpc-addr = "%s:9090"\n' "$host" >> "$file"
  fi
}

set_client_toml "$NODE1_HOME/config/client.toml" "chain-1"
set_client_toml "$NODE2_HOME/config/client.toml" "chain-2"

strip_vrf_section() {
  file=$1
  awk '
    BEGIN{skip=0}
    /^\[vrf\]/{skip=1; next}
    /^\[/{if(skip==1){skip=0}}
    {if(skip==0) print}
  ' "$file" > "$file.tmp"
  mv "$file.tmp" "$file"
}

append_vrf_section() {
  file=$1
  addr=$2
  strip_vrf_section "$file"
  cat >> "$file" <<VRF_EOF

[vrf]
enabled = true
vrf_address = "$addr"
client_timeout = "10s"
metrics_enabled = false
VRF_EOF
}

append_vrf_section "$NODE1_HOME/config/app.toml" "sidecar-1:8090"
append_vrf_section "$NODE2_HOME/config/app.toml" "sidecar-2:8090"

copy_drand() {
  home=$1
  src=$2
  rm -rf "$home/drand"
  mkdir -p "$home/drand"
  cp -R "/var/cosmos-chain/drand-fixture/$src/." "$home/drand/"
}

copy_drand "$NODE1_HOME" "$DRAND_NODE1"
copy_drand "$NODE2_HOME" "$DRAND_NODE2"

write_vrf_toml() {
  home=$1
  data_dir=$2
  public_addr=$3
  private_addr=$4
  control_addr=$5

  cat > "$home/config/vrf.toml" <<VEOF
allow_public_bind = true
data_dir = "$data_dir"
public_addr = "$public_addr"
private_addr = "$private_addr"
control_addr = "$control_addr"
dkg_beacon_id = "default"
dkg_threshold = $THRESHOLD
dkg_period_seconds = $PERIOD_SECONDS
dkg_genesis_time_unix = $GENESIS_UNIX
dkg_timeout = "1h"
dkg_catchup_period_seconds = $CATCHUP_SECONDS
VEOF
}

write_vrf_toml "$NODE1_HOME" "$NODE1_HOME/drand" "$PUB_ADDR1" "$PRIV_ADDR1" "$CTRL_ADDR1"
write_vrf_toml "$NODE2_HOME" "$NODE2_HOME/drand" "$PUB_ADDR2" "$PRIV_ADDR2" "$CTRL_ADDR2"

EOF
)

  docker run --rm \
    -e CHAIN_ID="$CHAIN_ID" \
    -e DENOM="$DENOM" \
    -e KEYRING_BACKEND="$KEYRING_BACKEND" \
    -e TIMEOUT_COMMIT="$TIMEOUT_COMMIT" \
    -e BLOCK_GAS_LIMIT="$BLOCK_GAS_LIMIT" \
    -e MIN_GAS_PRICES="$MIN_GAS_PRICES" \
    -e VOTE_EXT_HEIGHT="$VOTE_EXT_HEIGHT" \
    -v "$NODE1_HOME":/var/cosmos-chain/node1 \
    -v "$NODE2_HOME":/var/cosmos-chain/node2 \
    -v "$DRAND_DIR":/var/cosmos-chain/drand-fixture \
    --entrypoint sh "$CHAIN_IMAGE" -lc "$INIT_SCRIPT"
fi

compose_cmd() {
  DEMO_DIR="$DEMO_DIR" \
  CHAIN_IMAGE="$CHAIN_IMAGE" \
  SIDECAR_IMAGE="$SIDECAR_IMAGE" \
  CHAIN_ID="$CHAIN_ID" \
  DENOM="$DENOM" \
  MIN_GAS_PRICES="$MIN_GAS_PRICES" \
  $DOCKER_COMPOSE -f "$COMPOSE_FILE" "$@"
}

compose_ps() {
  compose_cmd ps -q "$1"
}

compute_target_round() {
  python3 - <<PY
import json, time
with open("${DRAND_DIR}/metadata.json", "r") as fh:
    meta = json.load(fh)
period = int(meta.get("period_seconds", 0) or 0)
genesis = int(meta.get("genesis_unix_sec", 0) or 0)
safety = int(meta.get("safety_margin_seconds", 0) or 0)
if period <= 0:
    print(0)
    raise SystemExit
t = int(time.time()) - safety
if t < genesis:
    print(0)
    raise SystemExit
round_ = (t - genesis) // period + 1
if round_ < 1:
    round_ = 1
print(round_)
PY
}

wait_for_drand_round() {
  service=$1
  port=$2
  round=$3

  cid=$(compose_ps "$service")
  if [ -z "$cid" ]; then
    echo "missing container for $service" >&2
    exit 1
  fi

  echo "waiting for $service drand round $round (timeout ${DRAND_WAIT_TIMEOUT}s)..."
  i=0
  while [ "$i" -lt "$DRAND_WAIT_TIMEOUT" ]; do
    if docker exec -e DRAND_PORT="$port" -e DRAND_ROUND="$round" "$cid" sh -lc 'python3 -c "import os,urllib.request,sys; r=int(os.environ[\"DRAND_ROUND\"]); port=os.environ[\"DRAND_PORT\"]; url=f\"http://127.0.0.1:{port}/public/{r}\"; urllib.request.urlopen(url, timeout=2).read()"' >/dev/null 2>&1; then
      echo "$service drand round $round is ready."
      return 0
    fi
    i=$((i+1))
    sleep 1
  done

  echo "timed out waiting for $service drand round $round" >&2
  return 1
}

compose_cmd up -d sidecar-1 sidecar-2 chain-1 chain-2
TARGET_ROUND=$(compute_target_round)
if [ "$TARGET_ROUND" -gt 0 ]; then
  wait_for_drand_round sidecar-1 8081 "$TARGET_ROUND"
  wait_for_drand_round sidecar-2 8082 "$TARGET_ROUND"
fi

cat <<EOF
Demo is up.

Chain-1 RPC:  http://localhost:${CHAIN1_RPC_PORT:-26657}
Chain-2 RPC:  http://localhost:${CHAIN2_RPC_PORT:-26667}
Sidecar-1:    localhost:${SIDECAR1_GRPC_PORT:-8090}
Sidecar-2:    localhost:${SIDECAR2_GRPC_PORT:-8092}

To reset everything:
  RESET=true $0
EOF
