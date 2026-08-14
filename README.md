# sni-router

TCP router that peeks at the TLS ClientHello, picks a backend from the SNI, optionally writes a PROXY v2 header, then copies bytes both ways.

Typical path: client → sni-router → HAProxy (certificates) → backends.

## Routing

Load a YAML list from `ROUTING_CONFIG_PATH` (default `/etc/sni-router/routing.yaml`). A `default` route is required. Startup fails on an empty domain, `port <= 0`, a bad regex, or a duplicate `default` / `non-tls`.

```yaml
- domain: prom.example.com
  host: 10.0.0.1
  port: 8443
  useProxy: true
  dialTimeout: 5
- domain: '.*\.internal\.example\.com'
  host: 10.0.0.2
  port: 443
  useRegex: true
- domain: non-tls
  host: 127.0.0.1
  port: 80
- domain: default
  host: 127.0.0.1
  port: 443
```

| Field | Meaning |
| --- | --- |
| `domain` | Exact SNI, a regex when `useRegex` is true, or the special names `default` and `non-tls` |
| `host` | Backend host (default `127.0.0.1`) |
| `port` | Backend port (required, must be `> 0`) |
| `useRegex` | Treat `domain` as a regular expression |
| `reverseMatch` | Invert the match (warns if used without `useRegex`) |
| `useProxy` | Prepend PROXY protocol v2 before the ClientHello |
| `dialTimeout` | Backend dial timeout in seconds (default `10`) |

TLS with no SNI still uses the TLS routes (`default` if nothing else matches). Broken TLS is closed; it is not sent to `non-tls`.

## Environment

| Variable | Default | Meaning |
| --- | --- | --- |
| `LISTEN_PORT` | `443` | TCP listen port |
| `LISTEN_ADDR` | empty (all interfaces) | Bind address |
| `METRICS_PORT` | `9113` | Prometheus `/metrics` port |
| `ROUTING_CONFIG_PATH` | `/etc/sni-router/routing.yaml` | Route file |
| `MAX_CONNECTIONS` | `0` (unlimited) | In-flight cap; extras are closed and counted as `max_conns` |
| `DROP_UID` / `DROP_GID` | `0` (no change) | Drop privileges after bind |
| `SHUTDOWN_TIMEOUT_SECONDS` | `30` | Drain time on SIGTERM/SIGINT; leftovers are then closed |

In Kubernetes, set `terminationGracePeriodSeconds` higher than `SHUTDOWN_TIMEOUT_SECONDS`.

## Signals

- **SIGHUP** reloads the YAML and swaps routes. A bad file is logged; the previous routes stay in place. Connections already open keep the router they started with.
- **SIGTERM** / **SIGINT** stop accepting, wait for in-flight copies, then force-close anything still open.

## Metrics

Prometheus scrapes `http://<host>:9113/metrics`.

SNI labels on parse and outbound series are `present`, `empty`, or `error` — not hostnames.

`sni_router_connection_errors_total{reason=...}` counts failures:

| `reason` | When |
| --- | --- |
| `sni_parse` | ClientHello / SNI extract failed |
| `no_route` | No matching route (including missing `non-tls`) |
| `dial` | Backend dial timeout or failure |
| `max_conns` | Rejected because `MAX_CONNECTIONS` is full |
| `proxy_header` | PROXY v2 write failed |

Import `grafana/dashboard.json` for the bundled dashboard.

## Build and run

Go 1.26.6.

```bash
go test -race ./...
go build -o sni-router ./cmd
ROUTING_CONFIG_PATH=./routing.yaml LISTEN_PORT=8443 ./sni-router
```

Docker image `samssh1/sni-router` is published on version tags `v*.*.*`. Mount the route file at `/etc/sni-router/routing.yaml` or set `ROUTING_CONFIG_PATH`. Bind 443 as root if needed, then set `DROP_UID` / `DROP_GID`.
