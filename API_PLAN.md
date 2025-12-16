# API single source-of-truth plan (`./api`)

## Goal

Make the generated protobuf code in `./api/**` the *only* protobuf Go types used by the repo (chain module, ABCI vote extensions, and sidecar). Remove gogo-generated `*.pb.go` types as a dependency and stop generating them.

Non-goals:
- Redesigning proto schemas (field numbers, semantics).
- Changing on-chain state encoding formats unless required.
- Editing generated files (`*.pb.go`, `*.pulsar.go`, `*.pb.gw.go`).

## Current state (what we have today)

- Chain module protos (`proto/vexxvakan/vrf/v1/*.proto`) generate:
  - gogo types into `x/vrf/types/*.pb.go` (used across the chain)
  - pulsar/protov2 types into `api/vexxvakan/vrf/v1/*.pulsar.go` (+ `*_grpc.pb.go`) (used by sidecar client code)
- Vote extension proto (`proto/vexxvakan/vrf/abci/v1/vote_extension.proto`) generates:
  - gogo types into `x/vrf/abci/ve/types/vote_extension.pb.go` (used in consensus handlers)
  - pulsar/protov2 types into `api/vexxvakan/vrf/abci/v1/vote_extension.pulsar.go`
- Sidecar proto (`proto/vexxvakan/sidecar/v1/vrf.proto`) generates:
  - gogo types into `sidecar/servers/vrf/types/vrf.pb.go` (used by sidecar server/client)
  - pulsar/protov2 types into `api/vexxvakan/sidecar/v1/vrf.pulsar.go` (+ `*_grpc.pb.go`)

The main technical friction points when moving fully to `./api`:
- The pulsar/protov2 codegen does **not** respect some `gogoproto.*` options (notably `nullable=false`), so some fields that were values become pointers (e.g. `MsgUpdateParams.Params` becomes `*VrfParams`).
- Cosmos SDK message routing works fine with protov2 messages, but we must ensure:
  - all `sdk.Msg` implementations are registered in the InterfaceRegistry
  - gRPC-gateway routes are generated and registered for the new packages
  - any “ValidateBasic” logic currently implemented as methods on gogo structs is moved to regular functions (or inline msg-server validation).

## Key design decisions

### Decision A: Keep `./api` purely generated

Do **not** add hand-written `.go` files under `api/**`. `buf generate` for pulsar uses `clean: true` and may delete non-generated files in the output tree.

Implication: any adapters/validation/helpers live outside `api/` (e.g. in `x/vrf/types`, `x/vrf/abci/ve`, `sidecar/...`).

### Decision B: Stop relying on `msgservice.RegisterMsgServiceDesc`

`msgservice.RegisterMsgServiceDesc` relies on gogoproto’s global file/type registries. With protov2-only generation we should:

- manually register all message types and response types in `RegisterInterfaces` using:
  - `registry.RegisterImplementations((*sdk.Msg)(nil), &vrfv1.Msg..., ...)`
  - `registry.RegisterImplementations((*tx.MsgResponse)(nil), &vrfv1.Msg...Response{}, ...)`

This removes the need to keep gogo registries populated for our own protos.

### Decision C: Validation lives in msg/query servers (not on message methods)

Because protov2-generated messages are in `api/...` packages, we can’t attach `ValidateBasic()` methods. We will:

- move message validation to `x/vrf/types/validate_*.go` functions, and call them from msg server methods
- keep `ValidateGenesis` logic similarly as free functions

### Decision D: Deterministic encoding explicit in consensus-critical paths

For vote extensions and any data that is serialized and consumed by consensus logic:
- use deterministic protobuf serialization explicitly:
  - either `github.com/cosmos/gogoproto/proto.Marshal` (dispatches to deterministic protov2 for protov2 messages)
  - or `google.golang.org/protobuf/proto.MarshalOptions{Deterministic:true}.Marshal`

We should prefer being explicit in consensus code (ABCI vote extension encode/decode) so CodeQL and reviewers can see the determinism guarantee.

## Work plan (phased)

### Phase 0 — Prep & inventory (no behavior changes)

Deliverables:
- A list of all current imports of gogo-generated packages and the matching `api/...` replacement packages.
- A local branch that can toggle between gogo and api usage (compile-time) if we want a gradual migration.

Steps:
1. Inventory all gogo-generated package imports:
   - `github.com/vexxvakan/vrf/x/vrf/types`
   - `github.com/vexxvakan/vrf/x/vrf/abci/ve/types`
   - `github.com/vexxvakan/vrf/sidecar/servers/vrf/types`
2. Identify any direct dependencies on gogo-only APIs:
   - uses of `grpc1 "github.com/cosmos/gogoproto/grpc"` types
   - uses of `proto.RegisterFile`, `proto.MessageType`, etc. (should be eliminated for our code)
3. Decide migration order (recommended: vote-extension first, then sidecar, then chain module types).

Acceptance:
- No code changes yet; just a documented map of replacements.

### Phase 1 — Protobuf generation pipeline: “api has everything we need”

Goal: `./api` generation must include all artifacts currently consumed from gogo generation:
- protov2 message types (already)
- gRPC stubs (already)
- grpc-gateway stubs (missing today for `api` packages)

Steps:
1. Add `protoc-gen-grpc-gateway` to `proto/buf.gen.pulsar.yaml` so it outputs `*.pb.gw.go` alongside `*.pulsar.go` in `api/**`.
   - use `paths=source_relative`, `allow_colon_final_segments=true`, and `logtostderr=true` (matching the gogo template)
2. Keep `proto/buf.gen.gogo.yaml` temporarily for the transition, but remove it from the default `make proto-gen` flow once the repo compiles without gogo types.
3. Update `Makefile` targets:
   - `proto-gen` should run pulsar + openapi only (no proto-gogo)
   - keep a separate `proto-gogo` target if we want an escape hatch during migration

Acceptance:
- `make proto-gen` produces all required grpc/gateway code in `api/**` (and nothing required remains only under `x/vrf/types`).

### Phase 2 — Consensus vote extension types move to `api`

Scope:
- Replace `x/vrf/abci/ve/types` (gogo) with `api/vexxvakan/vrf/abci/v1` (pulsar/protov2).

Steps:
1. Update vote extension encode/decode:
   - `x/vrf/abci/ve/vote_extension.go`:
     - replace `vetypes` import with `vrfabci "github.com/vexxvakan/vrf/api/vexxvakan/vrf/abci/v1"`
     - encode/decode using deterministic marshal/unmarshal
2. Update all call sites:
   - `x/vrf/abci/preblock/vrf/preblock.go` and any other vote-extension consumers
3. Delete gogo-generated file tree `x/vrf/abci/ve/types` once nothing imports it.

Risks:
- None expected; message has no maps/oneofs; deterministic encoding is straightforward.

Acceptance:
- All consensus handlers compile and tests pass with vote extension type coming from `api/**`.

### Phase 3 — Sidecar server/client protos move to `api`

Scope:
- Replace `sidecar/servers/vrf/types` with `api/vexxvakan/sidecar/v1`.

Steps:
1. Update sidecar server registration to use the protov2 service interface and `Register*Server` from `api/...`.
2. Update sidecar client call sites:
   - `x/vrf/sidecar/*`
   - `cmd/sidecar/*`
3. Delete `sidecar/servers/vrf/types/vrf.pb.go` once unused.

Gotchas:
- gogo-generated stubs currently use `grpc1.ClientConn`; api stubs use `grpc.ClientConnInterface`.
- Ensure interceptors/middleware continue to work.

Acceptance:
- Sidecar builds and can serve requests using the `api/...` protobuf service.

### Phase 4 — Chain module protos move to `api`

Scope:
- Replace all uses of gogo-generated `x/vrf/types/*.pb.go` with `api/vexxvakan/vrf/v1`.

Key tasks:
1. Update module registration:
   - `x/vrf/module/module.go`:
     - Register services via `vrfv1.RegisterMsgServer` and `vrfv1.RegisterQueryServer`
     - Register grpc-gateway routes using the new `api/...` `RegisterQueryHandlerClient` (after Phase 1)
2. Update interface registration:
   - `x/vrf/types/codec.go`:
     - replace `msgservice.RegisterMsgServiceDesc(...)` with manual `RegisterImplementations` for:
       - all `vrfv1.Msg*` request types as `sdk.Msg`
       - all `vrfv1.Msg*Response` as `tx.MsgResponse`
     - update Amino registrations to reference `vrfv1.Msg*` types
3. Update keeper server implementations:
   - `x/vrf/keeper/msg_server.go`:
     - change method signatures to accept `*vrfv1.Msg...` and return `*vrfv1.Msg...Response`
     - move all `ValidateBasic()` usage to local validation functions
     - add nil checks for any formerly-non-nullable submessages (e.g. `msg.Params == nil`)
   - `x/vrf/keeper/grpc_query.go`: same pattern for query request/response types
4. Update genesis wiring:
   - `x/vrf/keeper/genesis.go`, `x/vrf/module/module.go` genesis functions:
     - switch to `*vrfv1.GenesisState`
     - adapt to pointer fields and `[]*T` repeated message fields
5. Update on-chain store codecs if needed:
   - If we keep JSON encoding in `x/vrf/types/collections_codec.go`, confirm marshaled bytes for stored values remain stable across the migration.
   - If we switch to protobuf binary encoding, plan and implement a store migration (this is a larger consensus-facing change).
6. Delete gogo-generated files after cutover:
   - `x/vrf/types/{tx,query,vrf,genesis}.pb.go`
   - `x/vrf/types/query.pb.gw.go`

Acceptance:
- `make build` succeeds with **no** repo code importing `x/vrf/types/*.pb.go` generated types.
- All existing tests still pass.

### Phase 5 — Cleanup, CI, and guardrails

Steps:
1. Remove gogo generation from default workflow:
   - remove `proto-gogo` from `Makefile` `proto-gen`/`proto-all` (or keep as “legacy” behind an explicit target)
   - remove `scripts/buf/buf-gogo.sh` if unused
2. Remove `x/vrf/types/*.pb.go` and other gogo outputs from version control.
3. Add a repo check that fails if gogo-generated VRF protobuf outputs reappear:
   - e.g. a CI step that asserts no files match:
     - `x/vrf/types/*.pb.go`
     - `sidecar/servers/vrf/types/*.pb.go`
     - `x/vrf/abci/ve/types/*.pb.go`
4. CodeQL configuration:
   - If CodeQL continues flagging `reflect` in generated `api/**` code, update CodeQL config to ignore generated paths (recommended: ignore `api/**` and any `*.pulsar.go` / `*.pb.go`).

Acceptance:
- Default developer flow is “generate api protos, build, test”.
- No gogo-generated protobuf code is required for the repo to build and run.

## Testing & verification checklist (run each cutover phase)

- `make proto-gen` (or `make proto-all` if it’s the standard)
- `make build`
- `make test`
- `make lint`

Consensus-specific verification:
- Add/keep a deterministic round-trip test for vote extension encoding:
  - same input => identical bytes across repeated runs
  - decode(encode(x)) == x for all fields

## Open questions (need answers before implementation)

1. **Do we want to change the `.proto` `go_package` options to match `github.com/vexxvakan/vrf/api/...`?**
   - Recommended long-term: yes (cleaner for external Go users).
   - Short-term: not required if we only care about in-repo usage with `paths=source_relative`.
2. **On-chain store encoding: keep JSON or migrate to protobuf?**
   - Keeping JSON minimizes consensus risk (no migration), but it’s non-standard and larger on disk.
   - Migrating to proto binary is more canonical but requires a state migration and careful review.
3. **Should we eliminate `gogoproto/gogo.proto` imports and options entirely?**
   - Optional cleanup; not required for this “api is the only Go types” goal.

