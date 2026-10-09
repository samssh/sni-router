# sni-router

TCP router that peeks at the TLS ClientHello, picks a backend from the SNI, optionally writes a PROXY v2 header, then copies bytes both ways.

Typical path: client → sni-router → HAProxy (certificates) → backends.

One process can listen on many `ip:port` addresses, each with its own routes on top of a shared set. This is meant for a VM with several (floating) IPs.

## Upgrading from v0.2

v1.0.0 is a breaking release:

- The route file is a map with `routes` and `listeners`. The v0.2 bare list is rejected with an explicit error.
- `LISTEN_ADDR`, `LISTEN_PORT`, `DROP_UID` and `DROP_GID` are removed. Startup fails if any of them is set.
- The image runs as UID 65532 and binds :443 through a file capability (see [Docker](#docker)).
- `listener` is a new label on the inbound, outbound and error metrics.

## Configuration

Load YAML from `ROUTING_CONFIG_PATH` (default `/etc/sni-router/routing.yaml`).

```yaml
routes:                          # shared by every listener
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

listeners:                       # at least one
  - addr: 203.0.113.10:443
  - addr: "[2a01:4f8:c2c:afd3::2]:443"
    maxConnections: 500
    routes:                      # checked before the shared routes
      - domain: api.example.com
        host: 10.0.0.9
        port: 443
  - addr: 203.0.113.12:8443
    inheritRoutes: false         # only its own routes
    routes:
      - domain: default
        host: 10.0.0.20
        port: 443
```

### Listeners

| Field | Meaning |
| --- | --- |
| `addr` | `ip:port` or `[ipv6]:port`. IP literals only, no hostnames. The normalized form is the `listener` label. |
| `maxConnections` | Optional in-flight cap for this listener (`0` = unlimited) |
| `inheritRoutes` | Default `true`. `false` uses only the listener's own `routes`. |
| `routes` | Listener routes, same fields as below |

A listener's routes are checked first, then the shared ones. A listener route replaces any shared route with the same `domain`, including `default` and `non-tls`. Each replacement is logged at `debug`.

Startup (and reload) fails on:

- a listener that ends up without a `default` route (its own or the shared one)
- a duplicate `addr` (after normalization, so `[2a01:04f8::2]` equals `[2a01:4f8::2]`)
- an overlap with a wildcard on the same port: `0.0.0.0:443` excludes every other IPv4 listener on 443, and `[::]:443` (dual-stack) excludes every other listener on 443
- an invalid route, a duplicate `default` / `non-tls` in one list, or an unknown field (typos are not ignored)

### Routes

| Field | Meaning |
| --- | --- |
| `domain` | Exact SNI, a regex when `useRegex` is true, or the special names `default` and `non-tls` |
| `host` | Backend host (default `127.0.0.1`) |
| `port` | Backend port (required, must be `> 0`) |
| `useRegex` | Treat `domain` as a regular expression |
| `reverseMatch` | Invert the match (warns if used without `useRegex`) |
| `useProxy` | Prepend PROXY protocol v2 before the ClientHello |
| `dialTimeout` | Backend dial timeout in seconds (default `2`) |

TLS with no SNI still uses the TLS routes (`default` if nothing else matches). Broken TLS is closed; it is not sent to `non-tls`.

The PROXY v2 destination is the address the client connected to on that listener, so HAProxy and xray see which IP was hit. With Docker port mapping (NAT) it would be the container address instead, so run with host networking.

## Environment

| Variable | Default | Meaning |
| --- | --- | --- |
| `ROUTING_CONFIG_PATH` | `/etc/sni-router/routing.yaml` | Config file |
| `METRICS_PORT` | `9113` | Prometheus `/metrics` port |
| `MAX_CONNECTIONS` | `0` (unlimited) | In-flight cap across all listeners; extras are closed and counted as `max_conns`. A connection must also fit its listener's `maxConnections`. |
| `SHUTDOWN_TIMEOUT_SECONDS` | `30` | Drain time on SIGTERM/SIGINT; leftovers are then closed. Whole seconds; validated at startup. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |

In Kubernetes, set `terminationGracePeriodSeconds` higher than `SHUTDOWN_TIMEOUT_SECONDS`.

## Binding

One listener never takes down the others:

- On Linux every socket uses `IP_FREEBIND` / `IPV6_FREEBIND`. A bind to an address that is not on an interface yet succeeds: a floating IP that was just unassigned, or an IPv6 address that is still tentative during duplicate-address detection at boot. Connections arrive once the address appears.
- A transient bind error (for example address in use) is logged and retried in the background with backoff up to 30s. `sni_router_listener_up` is `0` meanwhile.
- A permanent bind error (permission denied, address family not supported) is logged once and not retried. `listener_up` stays `0` until the next SIGHUP, which retries it.
- Startup fails if every listener fails permanently, so a misdeployed process exits instead of looking healthy while routing nothing.

Because FREEBIND makes a bind succeed even when the IP is missing, `listener_up` alone does not say the listener is reachable. `sni_router_listener_addr_present` is re-checked every 30s against the local interfaces, and a warning is logged whenever an address is or becomes missing. If the interfaces cannot be listed, the gauge is left as it was (or absent for a new listener) rather than reporting the address as missing. `listener_up == 1 and listener_addr_present == 0` means bound but unreachable, typically a floating IP that is not configured on the VM.

## Signals

- **SIGHUP** reloads the config and reconciles listeners:
  - new listeners are opened;
  - removed listeners stop accepting; their open connections keep running until they end;
  - existing listeners get the new routes and `maxConnections`; a lower cap applies at once, counting connections already open;
  - listeners whose bind failed permanently are retried;
  - an invalid file is logged and nothing changes.

  A connection accepted just before its listener was removed is still served.

  Connections already open keep the routes they started with. Rotating an IP is a config edit plus SIGHUP, with no restart and no dropped tunnels.
- **SIGTERM** / **SIGINT** stop every listener (`listener_up` drops to `0`), wait for in-flight copies, then force-close anything still open.

## Metrics

Prometheus scrapes `http://<host>:9113/metrics`.

The inbound, outbound and `connection_errors_total` series carry `listener="ip:port"`. SNI labels on parse and outbound series are `present`, `empty`, or `error`, not hostnames.

| Metric | Meaning |
| --- | --- |
| `sni_router_listener_up{listener}` | `1` bound and accepting, `0` not bound (retrying, failed permanently, or shutting down) |
| `sni_router_listener_addr_present{listener}` | `1` if the IP is on a local interface (always `1` for wildcards) |

When a listener is removed, its `listener_up` and `listener_addr_present` series go away immediately. Its connection series stay until its last connection closes, so open-connection totals keep adding up.

`sni_router_connection_errors_total{listener, reason}` counts failures:

| `reason` | When |
| --- | --- |
| `sni_parse` | ClientHello / SNI extract failed |
| `no_route` | No matching route (including missing `non-tls`) |
| `dial` | Backend dial timeout or failure |
| `max_conns` | Rejected because `MAX_CONNECTIONS` or the listener's `maxConnections` is full |
| `proxy_header` | PROXY v2 write failed |

Import `grafana/dashboard.json` for the bundled dashboard. It has a `$listener` variable and a listener status panel.

## Build and run

Go 1.26.6.

```bash
go test -race ./...
go build -o sni-router ./cmd
ROUTING_CONFIG_PATH=./routing.yaml ./sni-router
```

Binding ports below 1024 needs root or `CAP_NET_BIND_SERVICE`, e.g. `sudo setcap cap_net_bind_service=+ep ./sni-router`.

## Docker

Image `ghcr.io/samssh/sni-router` is published on version tags `v*.*.*`.

```bash
docker run -d --name sni-router --network host \
  -v /etc/sni-router:/etc/sni-router:ro \
  ghcr.io/samssh/sni-router:1.0.0
docker kill --signal=SIGHUP sni-router   # reload
```

- Use `--network host` so listeners bind the VM's real IPs and the PROXY header carries the right destination.
- The process runs as UID/GID 65532. The config file must be readable by that user.
- The binary has the file capability `cap_net_bind_service`, so it can bind :443 at any time, including listeners added on reload. The container must keep `NET_BIND_SERVICE`: Docker's default set has it; with `--cap-drop=ALL`, add `--cap-add=NET_BIND_SERVICE`. Without it the container does not start at all (`exec ./sni-router: operation not permitted`).
- `--security-opt no-new-privileges` is fine: it limits file capabilities to the ones the container already has, and `NET_BIND_SERVICE` is one of them.
- `docker kill --signal=SIGHUP` works under the non-root user: Docker delivers the signal to PID 1 either way.
