# Task completion checklist

Before handing off a change:

- Run formatting: `make format`
- Run lint: `make lint`
- Run unit tests: `make unit`
- If protobuf/API changes: `make proto-all`
- Ensure new behavior is deterministic for consensus-critical code paths.
