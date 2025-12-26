# VRF Vote Extensions

This package implements ABCI++ vote extensions for the VRF/drand beacon pipeline.

## Overview

Each validator runs (or connects to) a local `sidecar` process that can serve a drand beacon for a given round. During consensus, validators attach a `VrfVoteExtension` (protobuf) to their vote so that the network can deterministically agree on a beacon and write it to `x/vrf` state.

Important CometBFT semantics (v0.38.x):

- Vote extensions are visible to the **proposer** in `PrepareProposal` via `RequestPrepareProposal.LocalLastCommit` (`ExtendedCommitInfo`).
- Vote extensions are **not** provided to the application in `FinalizeBlock` (i.e. `RequestFinalizeBlock.DecidedLastCommit` is only `CommitInfo` / `VoteInfo`, without vote-extension bytes).

Because `PreBlock` runs during `FinalizeBlock` and must deterministically update state, the proposer injects the previous height’s `ExtendedCommitInfo` into the proposal tx list at index `0`. See `x/vrf/abci/proposals` and `x/vrf/abci/preblock/vrf`.

## ExtendVote

`ExtendVoteHandler` is called by CometBFT when the node is casting a vote. It:

1. Loads `x/vrf` params (and returns an empty extension when VRF is disabled).
2. Computes the deterministic target drand round using only on-chain data (no wall-clock time).
3. Queries `sidecar` for `Randomness(round = targetRound)` and encodes it as `VrfVoteExtension`.
4. Returns the encoded bytes to CometBFT for broadcast.

When `VrfParams.enabled == true`, **empty vote extensions are rejected** in `VerifyVoteExtension`. If a validator cannot reach its `sidecar`, its votes will not count; the chain will halt once >1/3 of voting power cannot provide beacons.

## VerifyVoteExtension

`VerifyVoteExtensionHandler` is called by CometBFT to validate vote extensions received from peers. It performs cheap, deterministic checks:

- Empty extensions are accepted.
- If `VrfParams.enabled == false`, non-empty extensions are accepted but ignored for randomness purposes.
- Otherwise, it:
  - Decodes the protobuf.
  - Checks basic field validity.
  - Optionally checks `chain_hash` against `x/vrf` params.
  - Enforces `randomness == SHA256(signature)` (cheap filter).

Full BLS verification of the drand beacon is performed later during `PreBlock` (see below).

## PreBlock aggregation (where state updates happen)

State updates are performed in `x/vrf/abci/preblock/vrf`, not in these vote-extension handlers:

- The proposer injects `ExtendedCommitInfo` into `Txs[0]` (see `x/vrf/abci/proposals`).
- `ProcessProposal` validates the injected payload (decode + vote-extension signature verification).
- `PreBlock` decodes the same injected `ExtendedCommitInfo`, verifies the drand beacon (BLS), enforces the >2/3 voting-power threshold, and writes the canonical beacon to `x/vrf`.

This keeps the live consensus path deterministic and makes `x/vrf` the canonical historical source for randomness.
