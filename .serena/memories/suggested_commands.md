# Suggested commands

## Build

- Build everything: `make build`
- Install everything: `make install`

## Tests

- Unit tests: `make test`

## Lint/format

- Lint: `make lint`
- Format: `make format`

## Protobuf

- Proto format+lint+gen: `make proto-all`
- Proto checks only: `make proto-check`
- Proto generation only: `make proto-gen`

## Dev environment

- Create dev stack: `make docker-up`
- Close dev stack: `make docker-down`
- Start sidecar-only: `make docker-sidecar-start`
- Stop sidecar-only: `make docker-sidecar-stop`
- Start chain-only: `make docker-chain-start`
- Stop chain-only: `make docker-chain-stop`
