# Changelog

## Unreleased

### Breaking

- SNI Prometheus labels are `present`, `empty`, or `error` instead of hostnames.
- `sni_router_sni_parsed_time_Seconds` is now `sni_router_sni_parsed_time_seconds`.
- Startup fails if the config is missing `default`, has an empty domain, `port <= 0`, a bad regex, or a duplicate `default` / `non-tls`.
- Broken TLS is closed. It is not forwarded to the `non-tls` route.
- Idle 30s deadlines are no longer applied after the SNI peek. Long-lived copies stay open.
- When `MAX_CONNECTIONS` is full, new connections are closed immediately instead of stalling `Accept`.

### Added

- `sni_router_connection_errors_total{reason=...}` for `sni_parse`, `no_route`, `dial`, `max_conns`, and `proxy_header`.
- SIGHUP reloads `ROUTING_CONFIG_PATH` without dropping in-flight copies.
- `LISTEN_ADDR`, `MAX_CONNECTIONS`, `DROP_UID` / `DROP_GID`, and `SHUTDOWN_TIMEOUT_SECONDS`.
- Per-route `dialTimeout` (seconds, default `10`).
- SIGTERM/SIGINT drain; leftovers are force-closed after the timeout.
- Connection `id` on inbound/outbound logs.
- TCP keepalives on accept and dial.
- Accept error backoff (10ms → 1s).
- Longer connection-duration histogram buckets (8h, 12h, 24h, 48h, 7d).
- Grafana dashboard in `grafana/dashboard.json`.
- Tests, `go test -race` CI, and a README.

### Fixed

- PROXY v2 is written synchronously before the ClientHello is forwarded, so HAProxy no longer sees TLS first and resets.
- ClientHello parsing is bounds-checked; empty, NUL, and oversized SNI are rejected.
- One-way copy half-closes the other side instead of a full close.
- Expected copy errors (EOF, closed conn/pipe) are not logged.
- Metrics bind failure is logged instead of crashing the process.
- Listen log uses the real bind address and port.

### Changed

- Go 1.26.6, `client_golang` v1.24.1, `go-proxyproto` v0.15.0.
- Docker image is `golang:1.26.6-alpine3.24` with `CGO_ENABLED=0`.
