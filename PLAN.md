# PLAN — PRD v2 Completion Checklist

This file is the canonical TODO list for reaching **100% compliance** with `PRD.md` (“Drand VRF Sidecar Integration – PRD v2 (Subprocess drand Model)”).

Conventions:
- `- [ ]` = TODO / not started
- `- [x]` = done
- Items tagged **(PARTIAL)** already exist but do not yet satisfy the PRD end-to-end; subtasks describe what’s missing.

---

## 0) Keep This Plan Honest (Process + CI)

- [ ] Add/maintain a “PRD coverage” mapping: every PRD **MUST/SHOULD** statement appears as a checkbox in this file (or is explicitly marked “out of scope” with justification).
- [ ] Add a lightweight `make prd-check` (or equivalent doc/test target) that runs:
  - [ ] `make lint-markdown`
  - [ ] `make proto-check` (if protobuf changes are in-flight)
  - [ ] `make lint`
  - [ ] `make test`
- [ ] Add a “Definition of Done” gate for PRD completion:
  - [ ] All checkboxes in this file either `[x]` or explicitly deferred with an owner + date.
  - [ ] `make build`, `make test`, `make lint` are green.
  - [ ] For protobuf/API changes: `make proto-all` is green.
  - [ ] For consensus-critical changes: relevant integration tests are green (see §8).

---

## 1) Sidecar (Subprocess drand Model) — `cmd/sidecar`, `sidecar/`

### 1.1 Safe-by-Default Networking (PRD §2.2.2, §3.3, §5.1)

- [x] Bind VRF gRPC to loopback/UDS by default; require explicit override for public bind.
- [x] Bind metrics to loopback by default; require explicit override for public bind.
- [ ] Add explicit “debug HTTP/JSON” server support on a **separate** address, disabled by default (**PRD requirement**):
  - [ ] New flags/config: `--debug-http-enabled`, `--debug-http-addr` (loopback/UDS only by default).
  - [ ] Ensure debug HTTP is never served on the production gRPC port (no h2c / no grpc-gateway on gRPC port).
  - [ ] Ensure debug HTTP refuses non-loopback bind unless `--vrf-allow-public-bind` (or a distinct `--debug-allow-public-bind`) is set.
- [x] Add Unix domain socket support for Prometheus metrics (PRD references loopback/**UDS** for metrics):
  - [x] Allow `--metrics-addr unix:///path`.
  - [x] Ensure filesystem permissions for socket are sane and documented.

### 1.2 Drand Endpoint Isolation / SSRF Resistance (PRD §2.2.4)

- [x] Ensure sidecar uses only statically configured drand endpoints.
- [x] Ensure VRF RPC requests cannot choose chain/base URL (no chain selector in request).
- [x] Ensure sidecar remains single-chain (no multi-chain code paths / selectors).
- [x] Ensure sidecar does not import `github.com/drand/drand/v2/internal/...` packages.
- [x] Enforce loopback-only drand HTTP endpoint (prevent outbound pivoting).
- [ ] Add explicit regression tests for “no dynamic routing” invariants:
  - [ ] `Randomness(round=...)` never influences the base URL/host/port/path beyond selecting `latest` vs `{round}`.

### 1.3 DoS Controls (PRD §2.2.4)

- [x] Global concurrency limit for VRF gRPC.
- [x] Global rate limiting for VRF gRPC.
- [x] Enforce at most one in-flight drand fetch (serialization).
- [x] Coalesce identical in-flight fetches (singleflight).
- [ ] Add **per-client** rate limiting (**PRD asks per-client and/or global; implement per-client to fully satisfy**):
  - [ ] Identify clients by `peer` IP for TCP and by a stable token for UDS.
  - [ ] Decide policy for `localhost` vs UDS vs NAT’d clients.
  - [ ] Add tests exercising per-client throttling without impacting other clients.
- [ ] Add gRPC server hardening knobs (keepalive / max conn age / max concurrent streams) for production deployments:
  - [ ] Document recommended settings for validators.

### 1.4 Drand Subprocess Supervision (PRD §2.2.1, §5.2, §6)

- [x] Start/stop drand as a child process when `--drand-supervise=true`.
- [x] Restart drand on unexpected exit with backoff.
- [x] Forward stdout/stderr to logger and expose subprocess health metric.
- [ ] Add operator-focused safety knobs:
  - [ ] Optional “no restart” mode for debugging.
  - [ ] Optional restart backoff ceiling/floor config.
  - [ ] Emit structured logs including exit code and restart count.

### 1.5 Drand Binary Version Pinning (PRD §3.1)

- [x] (PARTIAL) Sidecar can compare `drand version` output to `--drand-expected-version`.
- [ ] Make version pinning **mandatory by default** (PRD: refuse to run unknown drand semantics):
  - [ ] Embed an expected drand version (and, if available, build hash) into the sidecar build (ldflags or generated file).
  - [ ] Parse `drand version` output into `{semver, commit}` (tolerate format variations).
  - [ ] Fail startup if the discovered version/hash is not exactly the expected one (or outside an explicitly defined allowed range).
  - [ ] Provide an explicit escape hatch flag for development only (e.g. `--drand-version-check=off`) that is noisy in logs.
- [ ] Update build/packaging so the shipped sidecar release and shipped drand binary are always consistent.

### 1.6 Beacon Fetching, Verification, and Caching (PRD §2.2.1, §3.1, §5.2)

- [x] Fetch chain info from drand `/info` and validate it matches configured expectations.
- [x] Compute `randomness = SHA256(signature)` and return `{round, randomness, signature, previous_signature}`.
- [x] Verify returned `randomness` (if present) equals `SHA256(signature)` (cheap consistency check).
- [ ] Implement full drand BLS verification in sidecar (**PRD expects verified beacons**):
  - [ ] Use drand’s crypto scheme (`crypto.Scheme.VerifyBeacon`) against the configured drand group public key.
  - [ ] Require `previous_signature` where needed for chained verification (define correct behavior for round 1).
  - [ ] Return explicit errors for `bad_signature` vs `hash_mismatch` vs `wrong_round`.
  - [ ] Add unit tests with known-good beacon fixtures.
- [ ] Use drand’s helper for randomness derivation (`crypto.RandomnessFromSignature`) to avoid divergence from upstream semantics.
- [ ] Add an in-memory cache for “latest” beacon (PRD mentions caching latest beacon/seed):
  - [ ] Cache latest verified beacon + timestamp.
  - [ ] Serve `round=0` from cache where safe.
  - [ ] Ensure cache does not violate “single configured chain” invariants.

### 1.7 Sidecar Metrics & Logging (PRD §5.3)

- [x] Expose baseline Prometheus metrics for latest round, fetch totals, process health, time since success.
- [ ] Expand `vrf_drand_fetch_total{result=...}` labels beyond `success|failure` (PRD example reasons):
  - [ ] `timeout`, `http_error`, `not_found`, `decode_error`, `hash_mismatch`, `bad_signature`, `other`.
  - [ ] Ensure label cardinality is bounded and stable.
- [ ] Emit structured logs for each fetch attempt with `{round, chain_hash, result, error}`.
- [ ] Add metrics for gRPC server throttling:
  - [ ] Rate-limit rejections.
  - [ ] Concurrency-limit rejections / queue time (if implemented).

### 1.8 Sidecar ↔ Chain Awareness (Enabled + Reshare Watchers) (PRD §4.3.2, §5.5)

- [ ] Add an optional chain connection (RPC/gRPC/WebSocket) so sidecar can observe `x/vrf` state/events:
  - [ ] Flags/config: chain RPC address, polling interval, WS enablement, etc.
  - [ ] Fetch `VrfParams` at startup and treat as the source-of-truth for `{chain_hash, public_key, period, genesis, enabled, reshare_epoch}` (or define explicit precedence rules).
  - [ ] If the chain params disagree with local config, fail fast (operator misconfiguration).
- [ ] Implement “enabled watcher” (PRD §4.3.2 operational recommendation):
  - [ ] On `enabled == false`: gracefully stop managed drand subprocess.
  - [ ] On `enabled == true`: start/restart drand subprocess (if supervising) and resume serving.
  - [ ] Make behavior explicit when `--drand-supervise=false` (log only).
- [ ] Implement mandatory “reshare listener” (PRD §5.5):
  - [ ] Watch `VrfParams.reshare_epoch` (or events emitted by `MsgScheduleVrfReshare`).
  - [ ] When epoch increments, run configured drand resharing commands (`drand share --reshare ...`).
  - [ ] Ensure idempotency (don’t run twice for the same epoch).
  - [ ] Emit structured logs with triggering height/epoch and initiating address (from event if available).
  - [ ] Document required operator config and security considerations.

---

## 2) `x/vrf` Module State & APIs (PRD §4)

### 2.1 Params (`VrfParams`) Validation (PRD §4.1)

- [x] Enforce `safety_margin_seconds >= period_seconds`.
- [ ] Enforce presence of all timing/crypto fields when `enabled == true`:
  - [ ] `genesis_unix_sec` must be non-zero.
  - [ ] `chain_hash` non-empty.
  - [ ] `public_key` non-empty.
  - [ ] Consider validating `period_seconds` and `genesis_unix_sec` ranges (sanity only; deterministic).
- [ ] Ensure every on-chain param change path calls `VrfParams.Validate()` and fails invalid updates.

### 2.2 Beacon State Must Be Height-Scoped (PRD §4.2) — **MAJOR GAP**

PRD requires `latest_beacon` **and** `latest_beacon_height`, and consumers must treat randomness as available only when `latest_beacon_height == currentHeight`.

- [ ] Add `latest_beacon_height` to module state (collections item).
- [ ] Update protobuf + codegen for `latest_beacon_height`:
  - [ ] Add field(s) to `proto/vexxvakan/vrf/v1/genesis.proto` (and any query responses as needed).
  - [ ] Run `make proto-all` and commit regenerated outputs.
- [ ] Add `latest_beacon_height` to genesis (`GenesisState`) and wire into init/export.
- [ ] Update PreBlock to set both:
  - [ ] `latest_beacon = beacon`
  - [ ] `latest_beacon_height = H`
- [ ] Update keeper APIs:
  - [ ] `GetBeacon(ctx)` must fail unless `latest_beacon_height == ctx.BlockHeight()` (and `enabled == true`).
  - [ ] `ExpandRandomness` must inherit the same guarantee.
- [ ] Update gRPC queries and CLI:
  - [ ] Ensure queries at height `H` return randomness only if `latest_beacon_height == H` in that historical context.
  - [ ] Ensure current-height queries do not “fall back” to earlier beacons.

### 2.3 Round Mapping (`RoundAt`, target round) (PRD §4.2.1)

- [x] Implement deterministic `RoundAt(params, t)`.
- [x] PreBlock and ExtendVote compute `target_round(H)` from stored last-block-time and safety margin.
- [ ] Add unit tests for boundary conditions and jitter (PRD §8.1).
- [ ] Ensure “target round == 0” is treated as “VRF disabled for this block” everywhere:
  - [ ] ExtendVote returns empty extension.
  - [ ] PreBlock does not enforce threshold checks.
  - [ ] Consumers return “no randomness for block” (via `latest_beacon_height` rule).

### 2.4 Identity Binding + Cleanup Hooks (PRD §4.3)

- [x] Implement `MsgRegisterVrfIdentity` + on-chain storage.
- [x] Record `signal_unix_sec` and `signal_reshare_epoch`.
- [x] Implement staking hook cleanup on validator removal.
- [ ] Ensure identity binding is fully integrated with VRF slashing eligibility (see §6).

### 2.5 Committee Allow List + Privileged Control Plane (PRD §4.3.1)

- [x] Store committee allow list and treat module authority as always-allowed.
- [x] Add messages to add/remove committee members with labels.
- [ ] Add governance UX checks/tests ensuring committee membership is auditable and rotations are safe.

### 2.6 Emergency Disable (Tx + PreBlock + Ante) (PRD §4.3.2)

- [x] Implement shared `VerifyEmergencyMsg` used by Ante + PreBlock.
- [x] Enforce gasless, sequence-bypassed semantics for authorized emergency disable tx.
- [x] In PreBlock, bypass VRF enforcement for the block containing an authorized emergency disable and persist `enabled=false`.
- [ ] Ensure emergency disable does not accidentally allow fallback randomness:
  - [ ] Must be enforced via `latest_beacon_height` (see §2.2).

### 2.7 Reshare Scheduling (PRD §5.4–§5.5, §4.3.1)

- [x] Implement `MsgScheduleVrfReshare` to bump `VrfParams.reshare_epoch`.
- [ ] Ensure events emitted are sufficient for sidecar reshare listener to react deterministically (include `new_reshare_epoch`, initiator).

---

## 3) ABCI++ / Consensus Integration (PRD §2.2.3, §2.3.1, §7)

### 3.1 ExtendVote (PRD §2.2.3)

- [x] If `enabled == false` or `target_round == 0`, emit empty extension.
- [x] Fetch `Randomness(round=target_round)` from sidecar with a timeout; on failure emit empty extension.
- [x] Encode vote extension and attach to vote.

### 3.2 VerifyVoteExtension (PRD §2.2.3, §8.2)

- [x] Accept empty extensions by default.
- [x] If `enabled == false`, ignore non-empty extensions (do not reject votes based on VRF).
- [x] Deterministic “cheap checks”: decode, basic fields, chain hash match, `SHA256(signature) == randomness`.
- [x] Deterministic sanity check for round mismatch logs (do not reject to preserve liveness).
- [ ] Add unit tests for: hash mismatch rejection, decode errors, round sanity warnings (PRD §8.2).

### 3.3 Deterministic Vote-Extension Availability (PRD §2.3.1)

- [x] PrepareProposal injects encoded `ExtendedCommitInfo` into proposal tx list.
- [x] ProcessProposal validates injected payload and vote-extension signatures.
- [x] ProcessProposal bypasses the “injected payload required” rule when an authorized emergency disable tx is present.
- [ ] Add tests to cover:
  - [ ] Missing injected payload when VRF enabled -> reject.
  - [ ] Invalid injected payload -> reject.
  - [ ] Authorized emergency disable present -> accept without injected payload.

### 3.4 PreBlock Aggregation & Verification (PRD §2.2.3)

- [x] Compute `target_round(H)` from on-chain last-block-time and safety margin.
- [x] Decode vote extensions from injected `ExtendedCommitInfo`.
- [x] Filter to `drand_round == target_round`.
- [x] Verify `SHA256(signature) == randomness` and BLS verify beacon.
- [x] Require >2/3 voting power for valid beacons; require all valid beacons are identical; otherwise fail the block.
- [ ] Emit PRD-required events when randomness is set:
  - [ ] `vrf_randomness_set` with `{drand_round}` (and any other fields required by downstream tooling).
- [ ] Add explicit PreBlock failure metrics with `reason` labels (PRD §9.1):
  - [ ] `missing_beacon`, `hash_mismatch`, `bad_signature`, `inconsistent_extensions`.
- [ ] Record per-validator invalid/missing reasons for monitoring (PRD §10.2) and feed metrics (see §6.1).
- [ ] Update randomness storage to be height-scoped via `latest_beacon_height` (PRD §4.2) (see §2.2).

### 3.5 Vote Extensions Enablement & Upgrades (PRD §7)

- [ ] Document and/or wire consensus param `VoteExtensionsEnableHeight` for:
  - [ ] genesis enablement
  - [ ] live-chain upgrade enablement
- [ ] Add an upgrade playbook ensuring validators have time to deploy sidecar/drand before VE enablement.

---

## 4) Sidecar Client in the Node (PRD §2.2.2)

- [x] Implement a thin gRPC client to talk to sidecar (timeout-configurable).
- [ ] Ensure node config (`app.toml` `[vrf]`) includes:
  - [ ] `vrf_address`
  - [ ] `client_timeout`
  - [ ] `metrics_enabled` (if relevant on node side)
- [ ] Add docs/examples for validator operators (how to set sidecar address + timeouts).

---

## 6) Monitoring + Slashing (PRD §9–§10)

### 6.1 Monitoring Metrics (PRD §9.1, §10.2)

- [ ] Add `vrf_preblock_failures_total{reason=...}` and `vrf_preblock_success_total`.
- [ ] Add per-validator metrics:
  - [ ] `vrf_vote_extension_invalid_total{validator,reason}`
  - [ ] `vrf_vote_extension_missing_total{validator}`
- [ ] Ensure metrics label cardinality is controlled (validator label strategy, consensus address vs operator).
- [ ] Add dashboards (Grafana) and alerting rules (Prometheus) consistent with PRD guidance.

### 6.2 Slashing for VRF Non-Participation / Invalid Data (PRD §10.3) — **NOT IMPLEMENTED**

- [ ] Decide where slashing logic lives (extend `x/slashing` vs implement in `x/vrf` with hooks into slashing).
- [ ] Implement slashing params (conceptual in PRD):
  - [ ] `VrfSignedBlocksWindow`
  - [ ] `VrfMinParticipationRatio`
  - [ ] `SlashFractionVrfNonParticipation`
  - [ ] `SlashFractionVrfInvalidData`
  - [ ] `VrfSlashingEnabled`
- [ ] Implement VRF eligibility vs slashability rules (PRD §10.1):
  - [ ] Use `VrfParams.enabled`, `VrfParams.reshare_epoch`, identity signal epochs, and `slashing_grace_blocks`.
- [ ] Track participation per validator each height (bitset/counters) for VRF-slashable validators only.
- [ ] Enforce slashing/jailing once participation ratio drops below threshold.
- [ ] Enforce immediate slashing/jailing for objectively invalid VRF data when enabled.
- [ ] Ensure slashing logic is disabled whenever `VrfParams.enabled == false`.
- [ ] Add unit tests + integration tests for:
  - [ ] grace period behavior
  - [ ] participation ratio thresholding
  - [ ] invalid-data slashing path

---

## 7) Operational Readiness (PRD §6, §9)

- [ ] Document drand history retention requirements and recommended settings (PRD §6).
- [ ] Add an operator runbook for:
  - [ ] diagnosing transient drand delays (PRD §9.1)
  - [ ] emergency disable via governance (PRD §9.2)
  - [ ] “chain halted due to VRF” recovery playbook (PRD limitation note)
- [ ] Ensure release artifacts include:
  - [ ] sidecar binary
  - [ ] drand binary (pinned) or an explicit packaging contract
  - [ ] a way to validate both versions at runtime (PRD §3.1)

---

## 8) Testing & Validation (PRD §8)

- [ ] Round boundary unit tests (exact boundary + ±1s jitter) for `RoundAt` / target round (PRD §8.1).
- [ ] VerifyVoteExtension unit tests for deterministic sanity checks (PRD §8.2).
- [ ] Enable/disable semantics tests across:
  - [ ] ExtendVote/VerifyVoteExtension/PreBlock
  - [ ] keeper/gRPC behavior (PRD §8.3)
- [ ] Failure scenario simulations (devnet/integration) (PRD §8.4):
  - [ ] drand crashes/restarts
  - [ ] partial network partition / drand lag
  - [ ] deliberate wrong VRF data injection
  - [ ] validate halting behavior + monitoring visibility
