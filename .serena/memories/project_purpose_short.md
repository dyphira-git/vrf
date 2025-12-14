# Project purpose (short)

This repository implements a drand-based VRF/randomness pipeline for a Cosmos-SDK/CometBFT chain: a `sidecar` process that supervises/queries a local `drand` daemon and serves randomness via gRPC/HTTP, plus on-chain integration (ABCI++ vote extensions + `x/vrf` module) to deterministically verify/aggregate drand beacons and expose randomness to chain modules.
