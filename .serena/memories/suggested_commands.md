# Suggested commands

## Build

- Build everything: `make build`
- Install everything: `make install`

## Tests

- Quick/local unit tests (during iteration): run only the package(s) you touched, e.g. `go test ./x/vrf/...` or `go test ./sidecar/...` (optionally narrow further with `go test ./path/to/pkg -run TestName`).
- Full unit test suite (final verification at end of task): `make unit`

## Lint/format

- Lint: `make lint`
- Format: `make format`

## Protobuf

- Proto format+lint+gen: `make proto-all`
- Proto checks only: `make proto-check`
- Proto generation only: `make proto-gen`

## Dev environment

- Start dev stack (chain + sidecar): `make docker-up` (or `docker compose up`)
- Stop dev stack (keeps containers): `make docker-stop` (or `docker compose stop`)
- Remove dev stack: `make docker-down` (or `docker compose down`)
- Remove dev stack + volumes: `make docker-down-clean` (or `docker compose down -v`)
