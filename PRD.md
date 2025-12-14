# Drand VRF Sidecar Integration – PRD v2 (Subprocess drand Model)

## 1. Goals & Non‑Goals

### 1.1 Goals

- Provide a secure, unbiased, publicly verifiable randomness source to `chaind` using a **validator‑operated drand network**.
- Run randomness aggregation out‑of‑process in a sidecar (`sidecar`) that:
  - Supervises a local `drand` daemon process (subprocess model).
  - Exposes a simple gRPC/HTTP API for `chaind` to consume randomness.
  - Does **not** import or depend on drand `internal/*` packages.
- Integrate randomness into consensus via **ABCI++ vote extensions**, so that:
  - Each validator includes randomness derived from the drand beacon in its votes.
  - The app aggregates randomness in `FinalizeBlock` and stores it in `x/vrf` state.
- Provide first‑class randomness APIs for:
  - Cosmos SDK modules (`x/vrf` keeper + gRPC queries).
- Align validator incentives with VRF participation by:
  - Making VRF vote extensions a **first‑class responsibility** of bonded validators when VRF is enabled.
  - Introducing targeted slashing for validators that repeatedly fail to participate in VRF (e.g. consistently missing or invalid vote extensions), in addition to rich monitoring/metrics for operators.
  - Explicitly favoring **strong cryptographic guarantees** over “best‑effort” liveness for randomness:
    - If the drand network or VRF pipeline fails to meet the required threshold of valid, verified beacons, the chain will prefer to **halt progress** (until VRF is restored or explicitly disabled) rather than accept unverified or stale randomness.

### 1.2 Non‑Goals

- Embedding the drand node directly via `github.com/drand/drand/v2/internal/...` in our Go code.
- Supporting multiple drand chains simultaneously; we target **exactly one canonical VRF chain** per network.
- Designing specific application‑level consumers (lotteries, leader election, etc.); those will build on top of `x/vrf`.

---

## 2. High‑Level Architecture

### 2.1 Components

1. **Validator‑Operated Drand Network**
   - A drand deployment whose participants are the validators of the `chaind` chain (or infrastructure under their control).
   - Deployed according to the upstream operator guide:
     <https://docs.drand.love/operator/deploy/>
   - Each validator hosts a `drand` daemon process that:
     - Participates in DKG and beacon generation.
     - Exposes:
       - A private gRPC control API.
       - A public HTTP/gRPC API for clients (sidecar).

2. **VRF Sidecar (`sidecar`) – Supervising drand Subprocess**
   - New Go binary in this repo.
   - Responsibilities:
     - Manage the lifecycle of a **local `drand` process**:
       - Start/stop the `drand` binary as a child process.
       - Provide config (data dir, ports, group file) on disk.
       - Optionally orchestrate DKG and resharing by driving the drand CLI or control API.
     - Act as a **drand client** for the local node:
       - Use drand’s public HTTP/gRPC APIs (no `internal/*` imports) to fetch:
         - Chain info (`/info` or gRPC `ChainInfo`).
         - Latest and specific round beacons (`/public/latest`, `/public/{round}`).
       - Verify beacons (optional; drand already provides BLS verification semantics).
       - Derive randomness bytes `randomness = H(signature)`.
     - Expose a **VRF gRPC/HTTP API** to `chaind`:
       - `Randomness` – latest beacon + seed.
       - `Info` – chain hash, round, metadata for debugging.
     - Expose Prometheus metrics for drand/VRF health.

3. **VRF Client in `chaind`**
   - Thin gRPC client that connects to the local `sidecar`.
   - Reads parameters from `app.toml` `[vrf]` section:
     - `vrf_address`, `client_timeout`, `metrics_enabled`.
   - Integrated with `RepublicApp` via:
     - ABCI++ vote extension handlers (`ExtendVoteHandler`, `VerifyVoteExtensionHandler`).
     - A PreBlock handler that writes randomness to `x/vrf` state.

4. **On‑Chain `x/vrf` Module**
   - New Cosmos SDK module:
     - Stores the latest canonical `VrfBeacon` in state (one per height).
     - Manages params (canonical drand `chain_hash`).
     - Provides keeper methods and gRPC queries to:
       - Fetch the beacon at the current / specified height.
       - Expand the VRF seed into N random words.

### 2.2 Data Flow

#### 2.2.1 drand Daemon → sidecar (Subprocess)

- Each validator runs:
  - A `drand` daemon process (binary from `github.com/drand/drand/v2/cmd/drand`).
  - A `sidecar` process.
- `sidecar` supervises the drand daemon:
  - Ensures the binary is present (packaged with the node or installed via tooling).
  - Starts the daemon with configured ports and data directories.
  - Optionally, uses drand’s CLI or gRPC control API to:
    - Join the DKG / resharing ceremonies.
    - Monitor health, status, and group info.
- For randomness fetch:
  - `sidecar` calls drand’s **public HTTP/gRPC APIs** on `localhost`:
    - `/info` or gRPC `ChainInfo` to obtain chain metadata (genesis, period, public key).
    - `/public/latest` and `/public/{round}` to fetch beacons.
  - `sidecar`:
    - Verifies beacons against the configured chain hash and public key.
    - Computes `randomness = SHA256(signature)` using drand’s crypto helpers.
    - Caches the latest beacon + seed in memory for efficient serving.

#### 2.2.2 sidecar → `chaind`

- `sidecar` exposes a VRF gRPC server on `vrf_address` (loopback or Unix domain socket only by default):
  - `Randomness(QueryRandomnessRequest) -> QueryRandomnessResponse`:
    - Request:
      - May include an optional `round` field.
    - Response:
      - When `round` is set:
        - Returns a fully populated beacon `{ drand_round, randomness, signature, previous_signature }` for the **requested** round if available and verifiable via drand’s crypto library, or an error if that round cannot be served.
      - When `round` is unset:
        - Returns `{ drand_round, randomness, signature, previous_signature }` for the **latest** beacon (intended for debugging or ad‑hoc inspection, not for production vote extensions).
  - `Info(QueryInfoRequest) -> QueryInfoResponse`:
    - Returns `{ chain_hash, public_key, period, genesis_time }`.
  - Any optional HTTP/JSON endpoints for debugging run on a **separate** loopback/UDS address and are disabled by default.
- `chaind`:
  - Maintains a client to `sidecar` with configurable timeout.
  - Treats errors / timeouts as non‑fatal (empty vote extension).

#### 2.2.3 `chaind` ↔ ABCI++ (with On‑Chain Verification)

- **ExtendVoteHandler**:
  - For each height H:
    - If `VrfParams.enabled == false`:
      - Skips contacting `sidecar` and returns an **empty** vote extension (VRF is globally disabled).
    - Otherwise:
      - Computes a deterministic **target drand round** for height H using only on‑chain data:
        - Let `Tref(H)` be the block time of the **previous** finalized block (height `H-1`), as recorded in state.
        - Let `Teff(H) = Tref(H) - VrfParams.safety_margin_seconds`.
        - Let `target_round(H) = RoundAt(Teff(H))` using the helper from §4.2.1.
        - If `target_round(H) == 0`, VRF is treated as disabled for this height (no drand round is yet safely usable), and the handler returns an empty vote extension.
      - If `target_round(H) > 0`:
        - Calls VRF client `Randomness(round = target_round(H))` to fetch the beacon for that specific round.
        - If successful:
          - Encodes it into a `VrfVoteExtension` proto and attaches to the vote.
        - If failure (timeout, unavailable, or drand has not yet produced that round locally):
          - Returns an **empty** vote extension (maintain liveness; block validity is decided later in `PreBlock`).
- **VerifyVoteExtensionHandler**:
  - Accepts empty extensions by default.
  - If `VrfParams.enabled == false`, must ignore any non‑empty VRF extensions for the purposes of randomness (but may log them as a configuration mismatch) and must not cause block rejection based solely on VRF content.
  - When `VrfParams.enabled == true` and the extension is non‑empty:
    - Decodes the vote extension.
    - Checks basic validity (non‑zero round, seed length).
    - Ensures chain hash (if included) matches the canonical one from `x/vrf` params.
    - Computes `SHA256(signature)` and checks that it matches the `randomness` field in the extension; if this cheap hash check fails, the handler must reject the vote extension and log the mismatch. This provides an early, deterministic filter for obviously malformed data without incurring the cost of full BLS verification.
    - Optionally performs additional **deterministic** sanity checks on the `drand_round` using only on‑chain data (e.g., the last finalized block time and `VrfParams{ period_seconds, genesis_unix_sec, safety_margin_seconds }`) to ensure the round is not implausibly far in the future or past relative to the chain’s own timeline. Implementations must **not** rely on local wall‑clock time for these checks; they should either reject clearly out‑of‑range extensions or accept them but emit structured warnings for operators.
- **PreBlockHandler**:
  - On `FinalizeBlock` at height H:
    - First, inspects the block proposal’s transactions in `RequestFinalizeBlock.Txs` to determine whether an authorized **emergency VRF disable transaction** is present at this height (see §4.3.2):
      - For each transaction in `req.Txs`, decodes it using the chain’s `TxConfig` and scans its messages for `MsgVrfEmergencyDisable`.
      - For each such message, runs a shared, deterministic authorization check (`VerifyEmergencyMsg`) that:
        - Verifies the transaction’s signatures under the chain’s configured sign‑mode.
        - Checks that at least one signer of the `MsgVrfEmergencyDisable` is present in the on‑chain VRF committee allowlist maintained by `x/vrf` (a `collections.Map[sdk.AccAddress, string]`). The module authority address is always considered a committee member.
      - If any `MsgVrfEmergencyDisable` in the block proposal passes `VerifyEmergencyMsg`:
        - `PreBlock` must treat VRF as **emergency‑disabled** for this block:
          - It must **not** attempt to compute `target_round(H)` or enforce VRF threshold/verification rules.
          - It must **not** write a `VrfBeacon` for height H.
        - As part of the block’s state transition, `x/vrf` updates `VrfParams.enabled = false`, so that subsequent heights behave as described below for the non‑emergency disabled case.
    - If VRF is disabled for this block (either because an authorized `MsgVrfEmergencyDisable` was included as above, or because `VrfParams.enabled == false` in state):
      - Must **not** attempt to aggregate or verify VRF vote extensions.
      - Must **not** fail the block based on missing or malformed randomness.
      - Should skip writing a `VrfBeacon` for this height, so that VRF consumers observe “no randomness available”.
    - If VRF is enabled for this block (`VrfParams.enabled == true` in state and no authorized emergency disable message was included in `req.Txs`):
      - Computes the same deterministic **target drand round** for height H as `ExtendVoteHandler`, using `Tref(H)` and `RoundAt(Teff(H))` as described above.
      - Collects all `VrfVoteExtension`s from the **extended commit** for height H via an application-level injection mechanism:
        - Upstream CometBFT v0.38 does **not** expose vote extensions to the application during `FinalizeBlock` (`RequestFinalizeBlock.DecidedLastCommit` contains `VoteInfo` but no `VoteExtension` bytes).
        - Therefore the proposer must inject the previous height’s `ExtendedCommitInfo` (from `RequestPrepareProposal.LocalLastCommit`) into the proposal’s tx list, and `PreBlock` decodes it from `RequestFinalizeBlock.Txs[0]`.
      - For each extension:
        - If it is empty, treats the corresponding validator as VRF‑non‑participating at this height.
        - If it has `drand_round != target_round(H)`, treats it as a wrong‑round extension for monitoring/slashing purposes and **excludes** it from beacon aggregation.
        - If it has `drand_round == target_round(H)`:
          - Checks that `randomness == SHA256(signature)`.
          - Constructs a drand `common.Beacon` (or equivalent) from `{ previous_signature, drand_round, signature }` and uses the chain’s configured scheme (`crypto.Scheme.VerifyBeacon`) with `VrfParams.public_key` / `VrfParams.chain_hash` to verify the BLS beacon for that round.
          - If either check fails, treats the extension as objectively invalid (for monitoring/slashing) and excludes it from aggregation.
      - Let `S(H)` be the set of validators whose extensions for height H:
        - Use `drand_round == target_round(H)`, and
        - Pass both the SHA256 and BLS verification checks.
      - PreBlock computes the total voting power represented by `S(H)` and compares it against the chain’s consensus commit threshold (e.g. > 2/3 of total bonded voting power at height H):
        - If `power(S(H))` is **below** the threshold:
          - PreBlock **must fail** for this block; no randomness is written for height H, and the block is considered invalid from a VRF perspective (consensus will attempt to repropose height H later).
        - If `power(S(H))` **meets or exceeds** the threshold:
          - All beacons in `S(H)` should be identical under a correct drand configuration. Implementations must enforce that all `(drand_round, randomness, signature)` triples in `S(H)` agree; if they do not, PreBlock must fail the block, as this indicates a serious misconfiguration or attempted VRF forgery.
          - On success, PreBlock selects this unique beacon, stores it in `x/vrf`’s `latest_beacon` collection item for height H, and emits events (`vrf_randomness_set`) with `{ drand_round }`.
      - Returning an error from `PreBlock` / `FinalizeBlock` follows default CometBFT semantics and is a consensus‑fatal application error; repeated VRF failures here should be treated operationally as a network halt condition until either drand/sidecar is restored or `VrfParams.enabled` is toggled off via governance.

#### 2.2.4 Sidecar Input‑to‑Network Isolation

To prevent `sidecar` from becoming a pivot for SSRF or unintended outbound connections, we enforce strict rules on how the sidecar chooses which drand endpoints to talk to:

1. **Static chain configuration only**
   - `sidecar` loads a static configuration at startup that includes one or more drand chains/endpoints (for our current design, exactly one VRF chain).
   - Each configured drand chain is identified by:
     - A canonical `chain_hash`.
     - A fixed base URL and/or gRPC endpoint for the local drand daemon.

2. **No dynamic routing / no chain selection in requests**
   - The design supports **exactly one** drand chain.
   - VRF gRPC APIs (e.g. `Randomness`) do **not** accept a `chain_hash` or any similar selector.
   - There is no way for a request to choose or alter which drand chain is used; `sidecar` always serves the single configured chain.

3. **No base URL override from requests**
   - Request inputs (e.g. RPC fields, HTTP query params) must **never** be used to:
     - Construct arbitrary drand URLs.
     - Override hostnames, ports, or paths used by the drand client.
   - All drand client calls are built solely from the static configuration loaded at startup.

4. **Rate limiting and concurrency controls**
   - `sidecar` must enforce:
     - A maximum number of concurrent VRF gRPC calls / streams (e.g. via a semaphore or connection limits).
     - Per‑client and/or global rate limits (requests per second) for VRF APIs.
     - At most **one in‑flight drand beacon fetch** to the underlying drand node at a time (single‑flight pattern), so that multiple node‑local callers are served by cached or coalesced results rather than triggering parallel upstream fetches.
   - These limits protect against accidental or malicious resource exhaustion (e.g. long‑lived gRPC streams or repeated `Randomness()` calls) and ensure that each validator node’s `sidecar` behaves as a simple, low‑fan‑out client in front of its local drand process.

5. **No multi‑chain support**
   - There is no plan or mechanism to support multiple drand chains in `sidecar`.
   - All code paths assume a single canonical drand chain, configured via `VrfParams` and sidecar config at startup.

### 2.3 Trust & Security Model

- The **drand network** is operated by the validator set; security assumptions:
  - Threshold BLS signatures give unbiased, publicly verifiable randomness.
  - No external third party is trusted beyond the validator set.
- `sidecar`:
  - Is a local trust component; if compromised, it can:
    - Censor or delay randomness.
    - But cannot forge valid drand signatures (assuming drand node keys are secure).
- `chaind`:
  - Treats **unverified or inconsistent randomness as consensus-invalid** for that block:
    - PreBlock must fail if it cannot verify a drand beacon using on‑chain params and drand’s crypto library.
    - This prevents stale or forged randomness from ever entering consensus state.
  - This design favors strong cryptographic guarantees over “best-effort” liveness for randomness:
    - Operationally, the drand network and `sidecar` must be configured so a valid beacon is available in time for each block.

#### 2.3.1 Determinism & Vote Extension Retention

- **Source of truth for VRF data in FinalizeBlock**:
  - Upstream CometBFT v0.38 exposes vote extensions to the proposer in `PrepareProposal` (`RequestPrepareProposal.LocalLastCommit`), but does **not** include them in `FinalizeBlock` (`RequestFinalizeBlock.DecidedLastCommit`).
  - Therefore, when vote extensions are enabled, the proposer injects the encoded `ExtendedCommitInfo` into the proposal’s tx list (`Txs[0]`). The injected bytes are treated as non-SDK tx bytes and are only used as deterministic input for `PreBlock`.
  - `ProcessProposal` must validate the injected payload (decode + vote-extension signature verification) and reject proposals with missing/invalid injected data, except when VRF is bypassed for that height via an authorized emergency disable transaction (see §4.3.2).
- **Determinism**:
  - Every full node executing `FinalizeBlock` at height H sees the same injected bytes in the proposal tx list and therefore the same set of `VrfVoteExtension`s.
  - Our PreBlock verification and aggregation logic operates purely on this data and on on‑chain params (`VrfParams`), so the resulting `VrfBeacon` is deterministic.
- **Pruning / state sync**:
  - We only consume vote extensions during the live consensus path at `FinalizeBlock` time.
- Historical queries for randomness use `x/vrf` state (`latest_beacon` at a given height via `--height`), not historical vote extensions.
  - Nodes that replay from disk rely on CometBFT’s blockstore (which includes commits and vote extensions for the range they replay); we do not impose additional retention requirements beyond the consensus engine’s normal block retention for nodes participating in consensus.

---

## 3. External Dependencies & Versioning

### 3.1 drand Binary & Go Library

- **Binary**:
  - We depend on the `drand` CLI binary (`github.com/drand/drand/v2/cmd/drand`).
  - Our build / packaging process must:
    - Build this binary (e.g., via `go build` or `go install` at build time).
    - Ship it alongside `sidecar` (same container / host), or otherwise make a **specific, supported** drand version available on `$PATH`.
  - On startup, `sidecar` should:
    - Invoke `drand version` (or an equivalent API) to obtain a precise version identifier and, where possible, a build hash.
    - Compare this against a **compiled‑in expected version/hash** (or a tightly‑constrained compatibility range) and:
      - Log the discovered version/hash.
      - Refuse to start if the discovered drand version does not exactly match the expected version/hash (or falls outside the allowed range), rather than running against an unknown or unsupported drand release.
    - This “checksum/compatibility” check ensures that all validators running a given `sidecar` release are also running the same drand binary semantics for beacon generation and verification.
- **Go library**:
  - We add `github.com/drand/drand/v2` to our `go.mod`.
  - Used for:
    - Types: `common.Beacon`, `common/chain.Info`, drand client interfaces.
    - Crypto helpers: `crypto.RandomnessFromSignature` and BLS verification primitives, in particular the canonical `crypto.Scheme.VerifyBeacon` which:
      - Reconstructs the signed message from `{ previous_signature, round }` using the chain’s configured digest function, and
      - Verifies the aggregated BLS signature against the drand group public key.
      - We do **not** roll our own “`VerifyBLS(signature, round, chain_pubkey)`” helper; instead we always delegate message construction and verification to drand’s library, passing the full beacon fields we carry (`previous_signature`, `round`, `signature`).
    - HTTP/gRPC client code where useful (e.g. using drand HTTP API clients or `common/client.Client`).

### 3.2 No `internal/*` Imports

- We **do not** import `github.com/drand/drand/v2/internal/...` packages from this repo.
  - Go’s `internal` visibility prevents this anyway (our module path differs).
  - All node control runs through:
    - The drand binary as a subprocess.
    - Public APIs (HTTP/gRPC, CLI).

### 3.3 Transport & Metrics

- gRPC: `google.golang.org/grpc` for `sidecar` ↔ `chaind`.
  - Production deployments should use gRPC over loopback or Unix domain sockets only.
- HTTP (optional, debug‑only): standard library + `github.com/gorilla/mux` for **separate** debug/JSON endpoints if needed:
  - These must be disabled by default and, when enabled, bound only to loopback/UDS.
- Prometheus: `github.com/prometheus/client_golang` for VRF/drand metrics, exposed on loopback/UDS only by default.
  - When using UDS, manage access via socket directory ownership and the sidecar process `umask` (e.g., create `/var/run/vrf` owned by `vrf:prometheus` with mode `0750`, and run sidecar with `umask 007` so the socket is group-readable).

---

## 4. On‑Chain Model & APIs

### 4.1 `x/vrf` Params (Verification & Round Mapping)

To enable full on‑chain verification of drand beacons and a safe mapping from block time to drand round, params must contain all cryptographic and timing context needed:

```proto
message VrfParams {
  // Canonical drand chain identifier for this network (bytes; typically the hash used by drand's APIs).
  bytes chain_hash     = 1;
  // Drand collective public key (BLS public key for the beacon).
  bytes public_key     = 2;
  // Drand beacon period in seconds.
  uint64 period_seconds   = 3;
  // Drand genesis time (UNIX seconds) for round 1.
  int64  genesis_unix_sec = 4;
  // Safety margin in seconds between the scheduled drand round time and the block time.
  // We only accept rounds whose scheduled time is <= block_time - safety_margin_seconds.
  uint64 safety_margin_seconds = 5;
  // Whether VRF is currently enforced by the chain.
  // When false, VRF is logically disabled: vote extensions are ignored for randomness,
  // PreBlock does not require a beacon, and VRF queries behave as if
  // no randomness is available for the block.
  bool   enabled              = 6;
  // Monotonically increasing epoch reflecting drand resharing rounds.
  // Incremented (e.g. via governance / x/vrf) whenever a new drand resharing
  // is scheduled or completed. Used together with validator VRF identity metadata
  // to determine from which epoch a validator could reasonably be expected to
  // participate in VRF (for slashing and monitoring purposes).
  uint64 reshare_epoch        = 7;
  // Global grace period (in blocks) before VRF non-participation / invalid-data
  // slashing can apply once VRF is enabled and validators have had an on-chain
  // opportunity to obtain a drand share. This parameter (sometimes referred to
  // as VrfGracePeriodBlocks) gives the network time to deploy and stabilize
  // drand/sidecar after initial VRF activation or major reshares.
  uint64 slashing_grace_blocks = 8;
}
```

- Stored as a `collections.Item[VrfParams]`.
- Set at genesis / via governance and must be consistent with the drand group used by validators.
- Implementations **must enforce the invariant**:

  ```text
  safety_margin_seconds >= period_seconds
  ```

  at all times. In practice this means:

  - The `VrfParams` validation logic must reject any configuration that violates this inequality.
  - Governance proposals or param‑change handlers that update `VrfParams` must call this validation and **fail** proposals that would set `safety_margin_seconds < period_seconds`.

  Enforcing this invariant on‑chain guarantees that validators never consider a drand round that might still be in progress, preventing race conditions where different nodes might otherwise select different rounds near a boundary.
- These params allow any node to deterministically compute:
  - Which drand rounds are **completed** by a given block time.
  - Which round is acceptable to use for that block, without ever requesting a future round.

### 4.2 `VrfBeacon` State

```proto
message VrfBeacon {
  uint64 drand_round  = 1; // drand round number
  bytes  randomness   = 2; // SHA256(signature)
  bytes  signature    = 3; // drand BLS signature (required)
  bytes  previous_signature = 4; // previous drand BLS signature; required for chained-scheme verification
}
```

- Managed via `cosmossdk.io/collections` under `StoreKey: vrf`:
  - A single `collections.Item[VrfBeacon]` (`latest_beacon`).
- At each height H, `latest_beacon` holds the beacon canonically associated with H.
- Historical access:
  - Via Cosmos SDK / CometBFT `--height`:
    - `chaind q vrf beacon --height H` (or equivalent gRPC metadata) returns `latest_beacon` at that height.

### 4.2.1 Round Mapping from Block Time (Safe Policy)

We need a deterministic, manipulation‑resistant way to map each block height `H` to a drand round to be used for that block, using only on‑chain data that is already finalized by the time votes and `FinalizeBlock` are executed.

Let:

- `params` = `VrfParams{ chain_hash, public_key, period_seconds, genesis_unix_sec, safety_margin_seconds, enabled, reshare_epoch }`.
- `period` = `params.period_seconds`.
- `genesis` = `params.genesis_unix_sec`.
- `margin` = `params.safety_margin_seconds`.

Define the drand helper:

```text
RoundAt(t) = 0,                         if t < genesis
           = floor((t - genesis) / period) + 1, otherwise.
```

For a height `H > InitialHeight`, we define a **reference time**:

- `Tref(H)` = block time of the previously finalized block at height `H-1`, as stored in state.

Using this reference time, we derive the target drand round for height `H` as follows:

1. Compute an **effective time** for round selection:

   ```text
   Teff(H) = Tref(H) - margin
   ```

   where `margin >= period` by configuration. This ensures we only ever consider rounds whose scheduled time is strictly before the reference time and gives a buffer for drand/network delays.

2. Compute the latest completed drand round by `Teff(H)`:

   ```text
   round_eff(H) = RoundAt(Teff(H))
   ```

3. The **target round** for height `H` is:

   ```text
   if round_eff == 0:
       // before drand genesis; no randomness available
       // (in practice, we should not enable VRF before this point)
       target_round = 0
   else:
       target_round = round_eff(H)
   ```

Policy:

- For blocks where `target_round == 0` **or** `params.enabled == false`, VRF must be treated as disabled for that block (e.g., during very early chain heights before drand genesis, or when governance has explicitly disabled VRF).
- For all other blocks (where `target_round > 0` and `params.enabled == true`), PreBlock must only accept beacons with `drand_round == target_round`:
  - Future rounds (`drand_round > target_round`) are **rejected** (cannot have occurred yet).
  - Older rounds (`drand_round < target_round`) are considered stale and also rejected.

Behavior if drand is slower than expected:

- For each height `H` with `params.enabled == true` and `target_round(H) > 0`, `ExtendVoteHandler` and `PreBlock` both target the same drand round `target_round(H)` derived from `Tref(H)`.
- If, at `FinalizeBlock` for height `H`, the commit’s vote extensions do not contain enough valid beacons for `target_round(H)` to reach the required voting‑power threshold (as described in §2.2.3), PreBlock **must fail** for that block:
  - We do not fall back to a previous or future round.
  - We do not reuse the previous height’s randomness.
- This couples chain liveness (for blocks that require randomness) to drand liveness and correctness, which is acceptable given the goal of a strong randomness guarantee. Operationally, validators must ensure drand is highly available and correctly configured, and that their `sidecar` instances can serve the requested `target_round(H)` reliably.
- Edge case – skipped drand rounds:
  - Under the chained BLS scheme used by drand, rounds are tied to time and assumed to be produced continuously. If the drand network ever **fails to produce a beacon for a specific round** `R` (e.g. it loses quorum exactly at that round and never recovers it), the chain will **not** move on to `R+1` or any later round for the corresponding heights whose `target_round(H) == R`.
  - From the chain’s perspective, a permanently missing round `R` is indistinguishable from drand being offline for that round: `sidecar` will fail to serve `Randomness(round = R)`, vote extensions will remain empty, and PreBlock will continue to fail for those heights until either:
    - drand eventually produces a beacon for round `R`, or
    - VRF is disabled via governance / out‑of‑band recovery as described in §9.2.
  - There is intentionally **no** policy to “skip” a round (e.g. accept `R+1` in lieu of `R`); this sharp edge reinforces the requirement that the drand network itself must maintain very high availability and avoid skipped rounds.

### 4.3 Validator–Drand Identity Binding

As in PRD v1, we need to bind validators to drand identities:

- Drand uses BLS12‑381; validator consensus keys are ed25519/secp256k1.
- We maintain:

```proto
message MsgRegisterVrfIdentity {
  string operator              = 1; // Bech32 account address controlling the validator
  bytes  drand_bls_public_key  = 2; // BLS12-381 public key/share
}

// Stored on-chain identity record (conceptual)
message VrfIdentity {
  string validator_address     = 1;
  bytes  drand_bls_public_key  = 2;
  bytes  chain_hash            = 3;
  // Block time (UNIX seconds) when this identity was first registered.
  int64  signal_unix_sec       = 4;
  // The value of VrfParams.reshare_epoch at the time this identity was first registered.
  // A validator is only expected to have a usable drand share (and therefore to be
  // able to participate in VRF) starting from the first resharing epoch that occurs
  // strictly after this signal epoch.
  uint64 signal_reshare_epoch  = 5;
}
```

Key points:

- `MsgRegisterVrfIdentity` is only valid if signed by the validator operator:
  - `GetSigners()` returns the address in the `operator` field.
  - The handler derives the validator operator address from `operator` (same address bytes, different bech32 prefix) and stores the mapping under that validator address.
- When `VrfParams.enabled == true`, VRF participation (i.e. producing correct vote extensions) is part of the validator’s expected behavior:
  - Validators that are in the active validator set but have not registered a drand identity (and therefore have no drand share) are still **VRF‑eligible** once VRF slashing is enabled; their lack of identity will manifest as persistent VRF non‑participation (empty/missing or invalid extensions) and may lead to VRF non‑participation penalties after a configured grace period.
  - Validators that have registered an identity but repeatedly fail to produce valid VRF vote extensions are treated as misbehaving for the purposes of monitoring and, if configured, slashing.
- The `x/vrf` module should integrate with `x/staking` via the standard staking hooks interface (`cosmos-sdk/x/staking/types.StakingHooks`):
  - In particular, it should implement `AfterValidatorRemoved(ctx, consAddr, valAddr)` and use this hook to **clean up** any `MsgRegisterVrfIdentity` mappings for validators that have fully unbonded or been permanently removed from the validator set.
  - This keeps the VRF identity store in sync with the long‑lived validator set and ensures validators that are no longer in the active set are not considered for future VRF participation tracking or slashing (even though, logically, eligibility is already restricted to the active set at each height).

#### 4.3.1 Reshare Scheduler Allow List

To control privileged VRF control-plane actions (reshare scheduling and emergency disable), `x/vrf` maintains a single committee allow list:

- On‑chain collection:
  - A `collections.Map[sdk.AccAddress, string]` under the `vrf` store key, mapping:
    - `sdk.AccAddress` → a free‑form, human‑readable label.
  - The label is intended for operator accountability and UX (e.g. `"vrf1..., marius"`), making it easy to audit and update the allow list as core teams change over time.
- Authorization:
  - Only addresses present in this committee are permitted to successfully submit `MsgScheduleVrfReshare` and authorize `MsgVrfEmergencyDisable`. Attempts from other addresses must be rejected by `x/vrf`.
  - Governance (via the module authority account) manages committee membership (add/remove entries). The module authority address is always considered a committee member by default.
- This design ensures that:
  - Reshare scheduling is centralized to a small, accountable set of operator addresses.
  - Cleaning up or rotating the reshare scheduling authority is as simple as updating the map (removing old entries and adding new ones with descriptive labels), without having to guess which bare addresses correspond to which humans or teams.

#### 4.3.2 Emergency VRF Disable via Transaction

To address the “bootstrapping paradox” (VRF‑induced chain halt preventing governance from disabling VRF) without relying on mempool contents or non‑deterministic inspection, the design defines a dedicated, **gasless emergency transaction** that PreBlock can recognize deterministically:

- Message type:

  ```proto
  message MsgVrfEmergencyDisable {
    string authority = 1; // Bech32 address of the signer requesting emergency disable
    string reason    = 2; // Free-form human-readable reason string
  }
  ```

- On‑chain committee allow list:
  - `x/vrf` maintains a single `collections.Map[sdk.AccAddress, string]` shared across reshare scheduling and emergency disable.
  - Only committee members are permitted to successfully trigger an emergency VRF disable.
  - The module authority address is always considered a committee member by default.

- Shared authorization function:
  - A helper `VerifyEmergencyMsg(ctx, tx, vrfKeeper) (authorized bool, reason string, err error)` must be implemented and used **both**:
    - In `PreBlock` (to decide whether to bypass VRF for this block), and
    - In the Ante/DeliverTx path (to accept or reject the transaction itself).
  - `VerifyEmergencyMsg` performs:
    - Full tx decoding using the chain’s `TxConfig`.
    - Signature verification under the chain’s configured sign mode.
    - For each `MsgVrfEmergencyDisable` in the tx:
      - Computes its signers via `msg.GetSigners()`.
      - Checks whether at least one signer is present in the VRF committee allow list in `x/vrf`.
      - If so, returns `authorized = true` and the `reason` field from the msg.
    - If no such message is found, or no signer is allow‑listed, returns `authorized = false`.
  - This shared function ensures that PreBlock and the normal transaction processing pipeline cannot disagree on whether a given emergency tx is “valid enough” to trigger the bypass.

- PreBlock behavior (see also §2.2.3):
  - At the start of `PreBlock` for height H, before any VRF logic:
    - Iterates over all transactions in `RequestFinalizeBlock.Txs`.
    - For each tx, calls `VerifyEmergencyMsg`.
    - If any call returns `authorized = true`:
      - PreBlock must treat VRF as **emergency‑disabled** for this block:
        - It must **not** compute `target_round(H)` or enforce VRF threshold/verification rules.
        - It must **not** write a `VrfBeacon` for height H.
      - As part of the block’s state transition, `x/vrf` updates `VrfParams.enabled = false` (and may record the `reason` in events), so that subsequent heights behave exactly as described for the non‑emergency disabled case (`enabled == false` from genesis).

- Ante/DeliverTx behavior:
  - A dedicated Ante decorator runs before fee/sequence checking:
    - If the tx contains a `MsgVrfEmergencyDisable` and `VerifyEmergencyMsg` returns `authorized = true`, the decorator:
      - Treats the message as **gasless** (no fee requirement).
      - Bypasses sequence/nonce checks for this msg (to avoid failures due to stale account numbers during emergencies).
      - Allows the tx to proceed to message handlers.
    - If `authorized = false`, the decorator rejects the tx.
  - The `x/vrf` message handler for `MsgVrfEmergencyDisable` may be a no‑op or emit events; the effective state change (toggling `VrfParams.enabled = false`) is already handled by `PreBlock` for the block that includes the authorized tx.

- Effects:
  - For the block at height H that first includes an authorized `MsgVrfEmergencyDisable`:
    - `PreBlock`:
      - Treats VRF as disabled for that block (no VRF checks, no beacon).
      - Updates `VrfParams.enabled = false` in state via `x/vrf`.
    - From height `H+1` onward:
      - `ExtendVote` / `PreBlock` must treat VRF as disabled (identical to §2.2.3 when `VrfParams.enabled == false` from genesis).
      - Validator‑local `sidecar` instances are expected to observe the change in `VrfParams.enabled` (e.g. via RPC polling or subscription) and **gracefully stop** their managed drand daemon for this chain, so that no further beacons are produced while VRF is disabled.
  - When VRF is later re‑enabled (e.g. via governance updating `VrfParams.enabled = true` after the underlying issues are resolved):
    - `ExtendVote` / `PreBlock` resume normal VRF behavior from that height onward, computing `target_round(H)` from the previous block time as usual and **never** attempting to backfill or reuse drand rounds from the interval during which VRF was disabled.
    - `sidecar` instances must detect the transition back to `enabled == true` and restart their drand subprocess for the chain so that fresh beacons are available again. This drand lifecycle coupling is an operational recommendation and does not affect consensus determinism, since the chain’s behavior is driven solely by `VrfParams.enabled` and the target‑round rules above.

### 4.5 Cosmos SDK‑Native Expansion

Keeper API:

```go
// GetBeacon returns the VrfBeacon for the current context height.
func (k Keeper) GetBeacon(ctx sdk.Context) (VrfBeacon, error)

// ExpandRandomness derives `count` random words from the beacon in the current context.
func (k Keeper) ExpandRandomness(
    ctx sdk.Context,
    count uint32,
    userSeed []byte,
) ([][]byte, error)
```

- Modules use `ctx` at execution time (current height).
- Historical queries use `--height` to get older seeds.
- If `VrfParams.enabled == false`, or there is no `VrfBeacon` for the current context height (e.g. PreBlock did not write one), `GetBeacon` **returns an error**, and `ExpandRandomness` must also fail with an error rather than silently synthesizing or reusing randomness.

gRPC query:

```proto
message QueryRandomWordsRequest {
  uint32 count     = 1;
  bytes  user_seed = 2;
}

message QueryRandomWordsResponse {
  uint64 drand_round = 1;
  bytes  seed        = 2;
  repeated bytes words = 3;
}
```

- Query server:
  - Uses the SDK context (which encodes height).
  - Calls `GetBeacon` and `ExpandRandomness`.
  - If `GetBeacon` fails because there is no beacon at that height, the RPC returns an error status; clients must treat this as “no randomness available for this block”.

---

## 5. sidecar Design (Subprocess Model)

### 5.1 Configuration & Flags

`sidecar` configuration should cover:

- Path to `drand` binary (or assume it’s on `$PATH`).
- Data directory for drand (per validator).
- Ports:
  - Drand private listen (P2P).
  - Drand public HTTP/gRPC listen (typically loopback / UDS only).
  - Drand control port (loopback only).
- VRF API listen address and metrics address:
  - **Must default** to loopback (`127.0.0.1`) or a Unix domain socket for production deployments.
  - The implementation should **refuse to start** if configured to bind the VRF gRPC API to a non‑loopback/public interface **unless** an explicit override/force flag is provided (e.g. `--vrf-allow-public-bind`), to prevent accidental exposure.
  - If operators deliberately expose `sidecar` beyond localhost, they are responsible for placing strict network ACLs and/or mTLS/TLS client authentication in front of the service; the default deployment model assumes `sidecar` is only reachable via local IPC and therefore performs no application‑level authentication.

Example flags:

```bash
sidecar \
  --drand-binary /usr/local/bin/drand \
  --drand-data-dir /var/lib/vrf/drand \
  --drand-public-addr 127.0.0.1:8080 \
  --drand-private-addr 0.0.0.0:4444 \
  --drand-control-addr 127.0.0.1:8881 \
  --vrf-api-address 127.0.0.1:9090 \
  --vrf-metrics-address 127.0.0.1:9091
```

### 5.2 Main Lifecycle (Pseudocode)

```go
func main() {
    cfg := LoadConfig()

    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer cancel()

    logger := NewLogger(cfg.LogLevel)
    metrics := NewMetricsRegistry()

    // 1. Start drand subprocess
    drandProc, err := StartDrandProcess(ctx, cfg.Drand, logger)
    if err != nil {
        logger.Fatal("failed to start drand", "err", err)
    }
    // Ensure that if sidecar exits (gracefully or due to a fatal error),
    // the child drand process is also terminated to avoid leaving orphaned
    // drand daemons running without supervision.
    defer drandProc.Stop()

    // 2. Create drand client bound to local drand HTTP/gRPC endpoints
    drandClient, chainInfo, err := NewDrandHTTPClient(cfg.Drand.PublicHTTPAddr, cfg.Params, logger)
    if err != nil {
        logger.Fatal("failed to create drand client", "err", err)
    }

    // 3. Validate drand chain configuration against on-chain params
    if err := ValidateDrandChainInfo(chainInfo, cfg.Params); err != nil {
        // Fail fast on obvious misconfiguration (e.g. chain hash, public key, period, or genesis mismatch)
        // rather than letting the node participate in consensus with silently invalid VRF data.
        logger.Fatal("drand chain info does not match on-chain VrfParams", "err", err)
    }

    // 4. VRF service wraps the drand client
    vrfService := NewVRFService(drandClient, logger, metrics)

    // 5. Start VRF gRPC server (production)
    go func() {
        if err := StartVRFGRPCServer(ctx, cfg.VRF, vrfService, logger); err != nil {
            logger.Error("vrf server exited", "err", err)
            cancel()
        }
    }()

    // 6. Optionally start a separate debug HTTP/JSON server (loopback/UDS only)
    if cfg.DebugHTTP.Enabled {
        go func() {
            if err := StartVRFDebugHTTPServer(ctx, cfg.DebugHTTP, vrfService, logger); err != nil {
                logger.Error("vrf debug http server exited", "err", err)
                cancel()
            }
        }()
    }

    // 7. Start metrics server
    go func() {
        if err := StartMetricsServer(ctx, cfg.Metrics, metrics, logger); err != nil {
            logger.Error("metrics server exited", "err", err)
            cancel()
        }
    }()

    <-ctx.Done()
    logger.Info("sidecar shutting down")
}
```

Key pieces:

- `StartDrandProcess`:
  - Builds the drand command line (`drand start ...` / `drand share` / etc.) based on config.
  - Manages process lifecycle and restarts if needed.
  - Logs stdout/stderr for debugging.
- `NewDrandHTTPClient`:
  - Wraps drand’s HTTP API:
    - `/info` to get chain info.
    - `/public/latest` to get latest randomness.
  - Optionally uses drand’s `common/client.Client` interface.
- `NewVRFService`, `StartVRFGRPCServer`, and `StartVRFDebugHTTPServer`:
  - `StartVRFGRPCServer` exposes the production gRPC API used by `chaind`:
    - Bound only to loopback or a Unix domain socket by default.
    - Does **not** use h2c or gRPC‑Gateway on this port.
  - `StartVRFDebugHTTPServer` (optional) can expose JSON/HTTP endpoints for debugging:
    - Runs on a **separate** loopback/UDS address.
    - Disabled by default and must be explicitly enabled in config.
    - If operators choose to expose it more widely, they are responsible for placing appropriate network ACLs / reverse proxies in front.

### 5.3 Logging & Metrics

`sidecar` must provide rich, operator‑friendly logging and Prometheus metrics around the drand/VRF pipeline:

- Logging:
  - Log when the drand subprocess starts, stops, or restarts, including exit codes and error messages.
  - Log each beacon fetch attempt at an appropriate level with at least `{ round, chain_hash, success/failure, error }`, e.g.:
    - “sidecar: fetched beacon round=X”
    - “sidecar: verification failed for round=Y: invalid BLS signature”
    - “sidecar: drand process not responding (timeout)”
  - Log configuration mismatches (e.g. chain hash or public key changes) and any validation failures when constructing the drand client.
- Metrics (examples; exact names may vary):
  - Gauge for the latest successfully verified drand round, e.g. `vrf_drand_latest_round`.
  - Counter for successful and failed beacon fetches, with reason labels, e.g. `vrf_drand_fetch_total{result=\"success|timeout|bad_signature|http_error\"}`.
  - Gauge or counter for time since last successful fetch, e.g. `vrf_drand_time_since_last_successful_fetch_seconds`.
  - Gauge for drand subprocess health, e.g. `vrf_drand_process_healthy` (`0/1`).
- These metrics and logs must be safe to emit from individual nodes (no impact on consensus) and are intended to power alerts and dashboards about drand health and VRF availability.
- Operators are expected to run `sidecar` and the Cosmos node under robust process supervision (e.g. `systemd`, container orchestrators), configure automatic restarts on failure, and use the metrics above to alert on situations where:
  - `sidecar` is down or repeatedly failing to start.
  - The drand subprocess is unhealthy or not producing beacons.
  - VRF beacons are not being fetched or verified successfully for a sustained period.

### 5.4 DKG / Resharing Strategy (Operator‑Managed)

- The drand network backing VRF is a **threshold BLS network**; it necessarily requires an initial Distributed Key Generation (DKG) ceremony and occasional resharing when membership changes significantly.
- This PRD does **not** require DKG or resharing to run for every consensus validator set change:
  - The drand group membership is expected to track a **long‑lived validator set** (or a subset operated by validators), and to change only when the operator community explicitly decides to run a resharing.
  - Conversely, the consensus validator set in `x/staking` can change frequently (bonding/unbonding, slashing, delegation changes).
- Responsibility is split as follows:
  - drand operators:
    - Use drand’s native CLI / APIs to run the initial DKG and any subsequent resharing ceremonies.
    - Maintain a drand group whose participants are a representative subset/superset of the chain’s validator community, but **do not** attempt to mirror per‑block consensus validator changes.
    - Ensure that the drand group file’s `chain_hash` and public key exactly match the on‑chain `VrfParams` (especially at genesis and after any resharing).
  - `chaind` / `x/vrf`:
    - Define **VRF‑eligible** validators strictly in terms of the active consensus validator set and on‑chain identity bindings (see §4.3 and §10.1).
    - Apply VRF participation tracking and any slashing logic only to validators that are in the consensus set at each height, independent of whether the drand group has a 1:1 mapping to that set.
- Operationally, validators are expected to:
  - Participate in the drand group when they join the long‑lived validator set (or arrange for infrastructure that does so on their behalf).
  - Update their drand participation as part of operator‑coordinated DKG/resharing events, not as an automatic consequence of every on‑chain staking change.

#### 5.4.1 Reshare Workflow (Informative)

At a high level, a typical resharing cycle proceeds as follows:

1. **Scheduling**:
   - An authorized scheduler (from the committee in §4.3.1) submits `MsgScheduleVrfReshare` (or equivalent), which:
     - Bumps `VrfParams.reshare_epoch` to a new value via `x/vrf`.
     - Emits an event that the `sidecar` reshare listener is watching for.
2. **Off‑chain resharing ceremony**:
   - Each validator’s `sidecar` instance, upon seeing the updated `reshare_epoch`, runs the configured drand resharing commands (e.g. `drand share --reshare ...`) against its local drand node.
   - The drand network executes the resharing protocol and, under the standard chained BLS scheme, produces a new threshold key set that:
     - Continues the same logical chain (same `chain_hash`), and
     - Maintains or updates the group membership and shares without changing the on‑chain `VrfParams.public_key` / `chain_hash` in the typical case.
3. **Post‑reshare validation**:
   - `sidecar` continues to fetch and verify beacons using the drand library and the on‑chain `VrfParams`.
   - `chaind` does not automatically change `VrfParams.public_key` or `chain_hash` as part of resharing; if a future drand upgrade requires a new group public key or chain hash, that must be handled as a separate, explicit on‑chain upgrade/param change, with appropriate coordination and testing.
4. **Slashability update**:
   - Once `VrfParams.reshare_epoch` has been incremented and the resharing has completed, validators with `VrfIdentity.signal_reshare_epoch <= VrfParams.reshare_epoch` now satisfy the “had an on‑chain opportunity to obtain a drand share” condition and become VRF‑slashable (subject to any global grace period), as defined in §10.1.

### 5.5 Resharing Signal and Mandatory Listener

To coordinate drand resharing in a controlled and accountable way, `sidecar` must include a long‑lived “reshare listener” that reacts to an on‑chain reshare signal. This listener is **mandatory** for validators that participate in the drand group once VRF slashing is enabled.

- Concept:
  - `sidecar` is configured with:
    - A `chaind` RPC endpoint (or WebSocket) to watch `x/vrf` state / events.
    - The drand CLI configuration required to run resharing commands.
  - The chain exposes an on‑chain reshare signal, e.g. a monotonically increasing `reshare_nonce` field or a dedicated `MsgScheduleVrfReshare` handled by `x/vrf`, that indicates “a new drand resharing round must be executed”.
  - When this signal changes (e.g. `reshare_nonce` increments), each validator’s `sidecar` instance:
    - Logs that a resharing has been requested, including the triggering height / nonce and the initiating address.
    - Invokes the configured drand CLI commands to participate in the resharing ceremony (e.g. `drand share --reshare ...`) using the local drand configuration and group file.
- Safety, authorization, and expectations:
  - The on‑chain reshare signal must only be modifiable by a small, explicit, and accountable set of addresses (see §4.3.1), so that “it’s time for a reshare” is a coordinated, permissioned decision rather than an open governance action.
  - For networks that enable VRF slashing and rely on drand resharing, validators who are part of the drand group are expected to:
    - Run `sidecar` with the reshare listener enabled in production, and
    - Ensure their infrastructure is capable of executing the drand resharing commands when signalled.
  - DKG/resharing remains an off‑chain, non‑deterministic process: failures or misconfigurations affect only the local drand node; consensus logic in `chaind` continues to rely solely on verified beacons and on‑chain `VrfParams`. Operators must treat a failed resharing (e.g. due to insufficient participation) as an operational incident and may need to coordinate a retry or intervene via governance before updating on‑chain params.

---

## 6. Risk & Implementation Notes

- **drand subprocess management**:
  - `sidecar` must handle:
    - Crashes and restarts.
    - Configuration drift (group updates, resharing).
  - For v1, we can assume DKG / resharing is orchestrated manually or via scripts; `sidecar` just starts an already‑configured drand node. These ceremonies are driven by operator coordination (e.g. when the long‑lived drand membership needs to change), **not** automatically on every consensus validator set update.
- **No `internal/*` usage**:
  - We treat drand as a black‑box daemon with documented APIs.
  - Any deeper integration (e.g. in‑process node) would require a separate design and likely a fork.
- For this network, we expect a relatively small and stable active validator set (on the order of tens of validators rather than hundreds). The design therefore assumes that **all active validators** are expected to participate in the drand group over time, maximizing decentralization of randomness. If, in the future, the validator set becomes very large or highly permissionless, networks may wish to revisit whether drand group membership should be restricted to a subset (e.g. top‑X validators) as a separate design exercise; such policies are out of scope for this PRD.
- **Height‑based lookups**:
  - If a node has pruned state at old heights, queries using `--height` for those heights will fail with the standard “pruned” error, same as any other module.
- **Validator deployment**:
  - Validators must:
    - Run `drand` and `sidecar` side by side.
    - Ensure `sidecar` is pointed at the correct drand ports and data dir.
    - Register their drand identity via `MsgRegisterVrfIdentity`.
  - Operators must provision monitoring and alerting based on the logs and metrics described above (both from `sidecar` and `chaind`) so that drand outages, verification failures, configuration mismatches, or frequent “no randomness” events are detected quickly. In practice this means:
    - Bundling `sidecar` and the drand binary into the validator’s deployment (e.g. Docker image, Helm chart) alongside the Cosmos node.
    - Using process supervisors (systemd, Kubernetes, etc.) to ensure that `sidecar` is always running and automatically restarted on crash, and that any unexpected exit is surfaced via logs/alerts rather than silently leaving the validator without VRF participation.
  - All participants must internalize that, under this design, **VRF and drand availability are part of the chain’s liveness assumptions**: even if >2/3 of voting power is online, blocks that require randomness will halt if those validators are not also providing valid, threshold‑satisfying VRF vote extensions. Keeping the drand network healthy and correctly configured is therefore as critical as keeping consensus nodes online.
  - Drand history retention:
    - Because `target_round(H)` is derived from the **previous block time** and does not advance when the chain is halted, a prolonged halt followed by restart may require validators to fetch a beacon for a round that is **hours old**.
    - Local drand daemons **must** be configured to retain enough beacon history to serve such requests (e.g., at least 24–48 hours of past rounds in typical deployments), otherwise `sidecar.Randomness(round = target_round(H))` will fail and the chain will remain stuck even after non‑VRF issues are resolved. Exact retention settings should be chosen conservatively relative to the expected maximum halt duration for the network.

---

## 7. Consensus Parameters (Vote Extensions Enablement)

As with any CometBFT/Cosmos SDK app, vote extensions must be enabled correctly so that:

- `ExtendVoteHandler` / `VerifyVoteExtensionHandler` are called at the right heights.
- Vote extensions are present in extended commits for `FinalizeBlock`.

### 7.1 New Chain (Genesis)

- At genesis, the consensus params’ `VoteExtensionsEnableHeight` must be set relative to the chain’s `InitialHeight`:

  - If VRF/vote extensions should be active from the first block:

    ```text
    VoteExtensionsEnableHeight = InitialHeight
    ```

  - If activation should be deferred until a later height `Hve`:

    ```text
    VoteExtensionsEnableHeight = Hve   // where Hve > InitialHeight
    ```

- CometBFT semantics:
  - Vote extensions become enabled at `VoteExtensionsEnableHeight`.
  - Once enabled at height `H`, vote extensions from `H` are available to the proposer for `H+1` and appear in the extended commit for `H`.

### 7.2 Upgrades on a Live Chain

- When enabling vote extensions via an upgrade handler:

  - If the current block height is `Hcurrent`, you must choose a future height:

    ```text
    VoteExtensionsEnableHeight = Hve   // with Hve > Hcurrent
    ```

  - The upgrade handler should:
    - Update consensus params to set `VoteExtensionsEnableHeight = Hve`.
    - Add the `x/vrf` store key via `StoreUpgrades` if it is not already present.

- This ensures:
  - Validators have a window to deploy and configure `sidecar` and their drand nodes before vote extensions become mandatory.
  - The ABCI++ lifecycle matches CometBFT’s expectations for when vote extensions become available and how they propagate from height `H` to `H+1`.

This PRD captures a drand integration strategy that mirrors the drand demo’s subprocess model, while staying within Go’s module boundaries and avoiding dependence on drand’s `internal/*` packages. It keeps the previously agreed on‑chain VRF design (module state, Cosmos SDK APIs) unchanged, only altering how randomness is sourced from drand.

---

## 8. Testing & Validation Notes

This section captures additional tests and validation scenarios that implementations should cover, beyond normal happy‑path unit tests.

### 8.1 Drand Round Boundary Conditions

- Tests for the `RoundAt` / target‑round mapping must include boundary cases where the block timestamp is **exactly** on a drand round boundary:
  - For a given `genesis_unix_sec`, `period_seconds = P`, and `safety_margin_seconds >= P`, construct a block time `T = genesis_unix_sec + N * P` for some integer `N`.
  - Verify that the effective time `Teff = T - safety_margin_seconds` results in a target round that corresponds to a **completed** drand round (i.e., the previous beacon relative to the round whose boundary is at `T`), and that all nodes deterministically compute the same target round.
- Include tests with small positive/negative jitter around the boundary (e.g., `T ± 1s`) to ensure off‑by‑one errors in `RoundAt` and the margin logic are not possible.

### 8.2 Vote Extension Sanity Checks

- Add tests for `VerifyVoteExtensionHandler` that cover:
  - Extensions where `randomness != SHA256(signature)` and assert that these are rejected and logged.
  - Extensions with `drand_round` values that are clearly out of range relative to the last finalized block time and `VrfParams` (too far in the past or future), verifying that the handler either rejects them deterministically or accepts them while emitting well‑structured warnings.
- These tests should ensure the lightweight checks in `VerifyVoteExtensionHandler` are **purely deterministic** (no dependence on local wall‑clock time) and that full cryptographic verification remains the responsibility of the PreBlock handler in `FinalizeBlock`.

### 8.3 VRF Enable/Disable Semantics

- Tests must cover transitions of `VrfParams.enabled` from `true -> false` and back:
  - When `enabled == false`, `ExtendVoteHandler` must stop producing VRF vote extensions, `VerifyVoteExtensionHandler` must ignore any non‑empty extensions for randomness, and `PreBlock` must not fail blocks due to VRF.
  - When `enabled` is re‑enabled, the handlers must resume their normal behavior, and blocks must again fail if required randomness is missing or invalid.
- Additional tests should ensure that VRF consumers (keeper, gRPC queries) observe the correct behavior:
  - With `enabled == false`, all randomness APIs must behave as if no beacon exists for the block (revert / error), even if historical beacons remain in state.
  - With `enabled == true`, and a valid beacon written by PreBlock, randomness APIs must succeed.

### 8.4 Failure Scenario Simulation (Recommended)

Beyond unit tests, implementers and operators are strongly encouraged to exercise the VRF/drand integration on a devnet under a variety of adverse conditions, including:

- Drand node crashes and restarts (single node and multiple nodes).
- Network partitions that cause some validators’ drand nodes to lag or temporarily lose quorum.
- Validators deliberately feeding wrong VRF data (e.g. malformed vote extensions, wrong rounds, wrong chain hash) to ensure:
  - `VerifyVoteExtension` rejects bad extensions deterministically.
  - `PreBlock` enforces the target‑round and threshold rules and halts blocks when necessary.
  - Monitoring, metrics, and slashing behave as specified.

These scenario tests act as a practical “proof of design” that the blueprint in this PRD matches real‑world behavior before VRF is enabled on mainnet.

---

## 9. Operational Tuning & Emergency Procedures

### 9.1 Monitoring Transient drand Delays

- A block failing because the target drand beacon is not yet available (when `VrfParams.enabled == true`) is acceptable as a **rare, transient** event: the network will attempt to propose the same height again a few seconds later, at which point the beacon is likely ready.
- To detect when drand is running “too hot” relative to block time (e.g., frequent “no randomness, block failed” events), `chaind` should:
  - Emit clear, structured error logs whenever `PreBlock` fails due to VRF, with at least:
    - `{ height, target_round, reason = missing_beacon | hash_mismatch | bad_signature | inconsistent_extensions }`.
    - Example: “vrf: randomness verification failed for height=H round=X reason=bad_signature”.
  - Expose Prometheus counters such as:
    - `vrf_preblock_failures_total{reason=\"missing_beacon|hash_mismatch|bad_signature|inconsistent_extensions\"}`.
    - `vrf_preblock_success_total`.
  - Optionally maintain a short‑window rate (via alerts or dashboards) of VRF‑related failures per N blocks.
- Operational guidance:
  - If missing‑beacon failures are more than a **rare** occurrence (e.g., multiple times per hour under normal network conditions), operators and governance should:
    - Investigate drand network health and validator configurations.
    - Consider increasing `safety_margin_seconds` via governance to give drand more time to produce beacons before the chain attempts to use them.

### 9.2 Emergency VRF Disable via Governance

- `VrfParams.enabled` provides a controlled, on‑chain “kill switch” for VRF enforcement, intended for emergencies such as prolonged drand outages or irrecoverable misconfiguration.
- Recommended emergency procedure:
  - Submit a parameter‑change governance proposal that sets `VrfParams.enabled = false` (leaving all other fields unchanged).
  - Once the proposal passes and is applied:
    - `ExtendVoteHandler` will stop emitting VRF vote extensions.
    - `VerifyVoteExtensionHandler` will ignore any VRF extensions for randomness (but may log them as a misconfiguration).
    - `PreBlock` will no longer fail blocks due to missing or invalid randomness and will skip writing new `VrfBeacon` entries.
    - VRF consumers (keeper, gRPC queries) will observe “no randomness available” for subsequent heights (errors / reverts), even though historical beacons remain queryable at earlier heights.
  - When the drand network is healthy again and operators are confident in the configuration, governance can submit another proposal to set `VrfParams.enabled = true` to restore full VRF enforcement.
- This mechanism allows the chain to remain live during extended drand outages while making the loss of randomness **explicit and visible** in parameters, logs, and metrics, rather than silently degrading security.
- Limitation:
  - Under default CometBFT semantics, if VRF failures in `PreBlock` / `FinalizeBlock` cause a **complete consensus halt**, the chain cannot process new governance proposals (including param changes) until operators perform an out‑of‑band recovery (e.g., coordinated upgrade or manual restart with VRF disabled in genesis). Protocol designers and operators should plan for this edge case and agree on a playbook for when VRF‑related failures cross the threshold from “rare transient” to “acceptable‑to‑halt and restart with VRF disabled”.

---

## 10. VRF Participation Monitoring & Slashing

This design includes objectively verifiable slashing paths for both **non‑participation** and **submission of invalid VRF data**, coupled with fine‑grained monitoring of validator behavior. More nuanced “who lied?” scenarios beyond these clearly attributable faults remain outside the scope of automated slashing in this iteration.

### 10.1 Definitions

- A validator is **VRF‑eligible** at height H if:
  - It is in the active consensus validator set at H, and
  - `VrfParams.enabled == true` at height H.
  - (Optionally, networks may also require `VrfSlashingEnabled == true` and that the global VRF activation grace period encoded in `VrfParams.slashing_grace_blocks` has elapsed since VRF slashing was first activated for the current drand epoch.)
- A validator is **VRF‑slashable** at height H if:
  - It is VRF‑eligible at H, and
  - `VrfSlashingEnabled == true`, and
  - It has had at least one **clear on‑chain opportunity** to obtain or refresh a drand share, given the network’s resharing signals:
    - Either it had an on‑chain `VrfIdentity` (with `signal_reshare_epoch <= VrfParams.reshare_epoch`) before or at the most recent drand resharing epoch; or
    - It has remained in the active set since before `VrfParams.reshare_epoch` was first set (i.e., was present for the initial DKG), and the number of blocks since VRF slashing activation for that initial DKG exceeds `VrfParams.slashing_grace_blocks`.
- A validator is **VRF‑participating** at height H if:
  - It is VRF‑slashable at H, and
  - Its vote for height H includes a `VrfVoteExtension` that:
    - Is non‑empty.
    - Passes `VerifyVoteExtensionHandler`’s deterministic checks (including `SHA256(signature) == randomness` and chain hash match).
    - Is consistent with the final beacon chosen by `PreBlock` (same `{drand_round, randomness, signature}`).
- A validator is **VRF‑non‑participating** at height H if it is VRF‑eligible but **not** VRF‑participating (for example, it sends an empty extension, an extension that fails validation, or no extension at all).

### 10.2 Monitoring Invalid and Missing Extensions

`chaind` must track and expose per‑validator statistics about VRF participation:

- For each height H where `VrfParams.enabled == true`:
  - `PreBlock` (or a helper invoked from it) records, for every VRF‑slashable validator:
    - Whether its extension was valid and matched the chosen beacon.
    - Whether it was invalid (failed verification) or missing/empty.
- Metrics (examples; labels include validator consensus address and reason):
  - `vrf_vote_extension_invalid_total{validator,reason}`:
    - Reasons might include `hash_mismatch`, `bad_signature`, `wrong_round`, `wrong_chain_hash`, `decode_error`.
  - `vrf_vote_extension_missing_total{validator}`:
    - Counts cases where a VRF‑eligible validator had no usable extension (empty or entirely missing).
- These metrics, combined with logs from `VerifyVoteExtension` and `PreBlock`, allow operators and tooling to identify validators that are consistently failing or misbehaving, even without automatic slashing.

### 10.3 Slashing for VRF Non‑Participation

To align incentives, the chain may configure automatic slashing for validators that repeatedly fail to participate in VRF and/or submit objectively invalid VRF data:

- Parameters (conceptual; exact wiring may reuse or extend `x/slashing`):
  - `VrfSignedBlocksWindow`: the number of recent heights over which VRF participation is tracked (e.g. similar to the standard signed‑blocks window).
  - `VrfMinParticipationRatio`: minimum required fraction of heights in the window at which a VRF‑eligible validator must be VRF‑participating (e.g. 0.9).
  - `SlashFractionVrfNonParticipation`: stake fraction to slash when a validator’s VRF participation ratio falls below the threshold.
  - `SlashFractionVrfInvalidData`: stake fraction to slash when a validator submits an objectively invalid VRF vote extension (see below).
  - `VrfSlashingEnabled`: feature flag to turn this behavior on/off independently of `VrfParams.enabled`.
- Behavior:
  - For each height in which `VrfParams.enabled == true`, `PreBlock` updates a per‑validator VRF participation bitset or counters (analogous to existing downtime tracking), but only **for VRF‑slashable validators**:
    - Mark `1` for VRF‑participating, `0` for VRF‑non‑participating.
    - Additionally, mark any VRF‑slashable validator that submitted a vote extension which was **objectively invalid** under the canonical drand parameters for that height, including:
      - `SHA256(signature) != randomness`.
      - BLS verification failure for `(drand_round, signature, VrfParams.public_key, VrfParams.chain_hash)` using the on‑chain drand crypto library.
      - `drand_round` not equal to the target round `target_round(H)` computed from `Tref(H)` and `VrfParams` as defined in §4.2.1 (i.e., future or stale rounds relative to the safe mapping).
  - Periodically (or incrementally), the module computes each VRF‑slashable validator’s VRF participation ratio over the last `VrfSignedBlocksWindow` heights.
  - If a VRF‑slashable validator’s participation ratio drops below `VrfMinParticipationRatio`, and `VrfSlashingEnabled == true`, the chain:
    - Executes a slashing event using `SlashFractionVrfNonParticipation`.
    - Jails the validator, requiring them to explicitly `unjail` once their VRF setup is corrected (exact unjailing rules mirror or extend existing downtime handling).
  - Separately, if a VRF‑slashable validator submits an objectively invalid VRF extension at any height (per the criteria above), and `VrfSlashingEnabled == true`, the chain:
    - Executes an additional slashing event using `SlashFractionVrfInvalidData`.
    - May immediately jail the validator, as this behavior is strongly indicative of malicious or severely misconfigured VRF participation (e.g., fabricating randomness or using an incorrect drand chain).
- Important constraints:
  - VRF non‑participation tracking and slashing are **disabled** whenever `VrfParams.enabled == false`.
  - The parameter `VrfParams.slashing_grace_blocks` provides a concrete, on‑chain **global VRF activation grace period** (sometimes referred to as `VrfGracePeriodBlocks`) around initial VRF activation and around any major drand resharing events. Implementations must ensure that VRF non‑participation / invalid‑data slashing does **not** apply to otherwise VRF‑slashable validators until this many blocks have elapsed since VRF slashing became active for the relevant drand epoch. After this grace period, validators that satisfy the **VRF‑slashable** definition above are expected to be fully VRF‑participating (have functioning drand/sidecar and, where required, registered identities), and failure to do so is treated as non‑participation or invalid data subject to slashing/jailing under the parameters above. Newly‑joined validators that have signalled a `VrfIdentity` but have not yet been included in any drand resharing (i.e., with `signal_reshare_epoch > VrfParams.reshare_epoch`) are VRF‑eligible but **not yet VRF‑slashable**; they are monitored for VRF behavior but only become slashable once a resharing has occurred after their signal or they satisfy the “present since initial DKG + grace period elapsed” condition, evaluated using `VrfParams.slashing_grace_blocks`.

### 10.4 Out‑of‑Scope Behaviors and Future Work

- This PRD v2 **intentionally limits** automated slashing to:
  - **Objective non‑participation** (absence of valid, matching VRF vote extensions for VRF‑slashable validators); and
  - **Objectively invalid VRF data** as defined above (hash mismatch, BLS verification failure under the canonical drand key/round, or use of a non‑target round).
- More nuanced behaviors remain out of scope for this iteration and are candidates for future work, including:
  - Distinguishing between edge‑case configuration drift and deliberate attempts to game the VRF timing policy when extensions are otherwise BLS‑valid.
  - Slashing policies that depend on off‑chain drand observations beyond the on‑chain parameters and verification logic already described.
- However, the per‑validator metrics and logs outlined above provide the necessary observability for governance or off‑chain coordination to take action against suspected malicious validators (e.g., governance‑driven jailing or stake removal) beyond the automated paths specified here.
