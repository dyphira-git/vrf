# sidecar

## gRPC server hardening knobs

The sidecar gRPC server (`--listen-addr`) is loopback/UDS-only by default. If you intentionally expose it beyond localhost (via `--vrf-allow-public-bind`), consider enabling stricter gRPC server limits to reduce exposure to resource exhaustion and long-lived abusive clients:

- `--grpc-max-connection-age=2h` and `--grpc-max-connection-age-grace=5m`
- `--grpc-max-concurrent-streams=64`
- `--grpc-max-recv-msg-size=1048576` and `--grpc-max-send-msg-size=1048576`
- `--grpc-keepalive-min-time=1m` (clients pinging more frequently are disconnected)

All `--grpc-*` flags default to `0`/`false`, which leaves gRPC's built-in defaults unchanged (or uses “infinity” for max-connection age knobs).
