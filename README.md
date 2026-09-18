# Proxy Leak Lab

> A self-hosted lab that exposes every channel through which your real IP address can leak past proxies and VPNs — and shows you the IP your server *actually* sees versus the one you *think* you're using.

## Why it exists

Even when you route traffic through an HTTP proxy, a SOCKS5 proxy, or a VPN, your real public IP can still leak through channels that bypass the proxy entirely:

- **WebRTC** — STUN server-reflexive (`srflx`) candidates reveal the NAT's public IP to the peer, regardless of proxy configuration.
- **Raw TCP / UDP** — applications that open sockets directly (not proxy-aware) send packets straight out to the destination.
- **DNS** — a resolver that isn't tunneled leaks queries to the recursive server of whoever operates the network.
- **IPv4 / IPv6 dual-stack** — one address family may route around the tunnel while the other stays inside it.
- **HTTP forwarding headers** — `X-Forwarded-For` and friends, when a proxy or CDN rewrites them.

Proxy Leak Lab turns each of these channels into an observable probe. Run it against a server you control and you get a side-by-side view of "the IP you intended to expose" versus "the IP the server actually observed".

## What it tests

| Leak channel | Probe |
|---|---|
| WebRTC STUN `srflx` | Browser WebRTC / ICE candidates |
| HTTP source IP + forwarding headers | Browser HTTPS + selected headers |
| IPv4 / IPv6 dual-stack | `v4.` and `v6.` HTTPS-only paths |
| HTTP/1.1, HTTP/2, HTTP/3 | Edge (Caddy) protocol visibility |
| Raw TCP | Direct TCP source IP |
| Raw UDP | Direct UDP source IP |
| STUN XOR-MAPPED-ADDRESS | Direct STUN probe |
| Proxy-aware vs proxy-unaware | Native client: explicit HTTP/SOCKS5 vs plain sockets |
| Per-IP geolocation | Local GeoLite2 country lookup |

It intentionally does **not** capture cookies, authorization headers, or request bodies.

## Architecture

| Component | Path | Role |
|---|---|---|
| Server | `server/` | Go HTTP backend plus raw TCP/UDP/STUN listeners; records every observation with local GeoIP; serves the embedded dashboard |
| Client | `client/` | Go CLI that fires proxy-aware and proxy-unaware probes and prints a color-coded summary |
| Dashboard | `server/index.html` | Dark card UI for browser tests (HTTP, WebRTC, IPv4/v6) and event inspection |

The server binds its HTTP backend to `127.0.0.1:8080` (a reverse proxy terminates public TLS in front of it) while raw TCP/UDP/STUN listeners bind publicly, because those probe channels must see the client's real source address.

## Quick start

```bash
cp .env.example .env
# set DASHBOARD_TOKEN (e.g. `openssl rand -hex 24`) and, optionally, MaxMind credentials
docker compose up -d
```

Open `https://leak.example.com`, paste the `DASHBOARD_TOKEN`, and run the browser tests. Each observed source shows as `IP:port — country (code)`.

> Full production deployment — DNS records, firewall/security groups, TLS certificates, systemd service, and nginx — is documented separately in [`docs/DEPLOYMENT_HANDBOOK.md`](docs/DEPLOYMENT_HANDBOOK.md).

## Client

Build on any machine with Go 1.23+:

```bash
go build -o leak-client ./client
```

```bash
./leak-client -domain example.com                          # direct probes
./leak-client -domain example.com -http-proxy http://127.0.0.1:7890
./leak-client -domain example.com -socks5 socks5://127.0.0.1:1080
./leak-client -domain example.com -socks5 socks5://127.0.0.1:1080 -skip-direct
```

The client prints one JSON report. Copy its `test_id` into the dashboard (or open `https://leak.example.com/?test_id=THE_ID`) to view browser and native events under a single identifier.

## Reading the results

- **`proxy-aware`** — the client explicitly used `HTTP_PROXY` / `HTTPS_PROXY` or the configured SOCKS5 proxy.
- **`proxy-unaware`** — the client opened ordinary TCP/UDP sockets. A system-wide TUN/VPN still routes these through the tunnel; an application-only HTTP/SOCKS proxy usually does **not**.

Seeing your normal ISP IP under `proxy-unaware` tests is expected when you only configured an application proxy — it is a leak only if your threat model requires *all* device traffic to use a full tunnel.

## Limitations

1. Browser STUN requests don't carry the dashboard `test_id`; correlate them by timestamp and the ICE candidate shown in the page.
2. Client SOCKS5 support is TCP CONNECT only (no UDP ASSOCIATE) in this version.
3. A true DNS-leak test requires an authoritative DNS server for a delegated test subdomain.
4. ICMP and traceroute need OS privileges and are not included in the cross-platform client.
5. Behind a CDN, the server sees and geolocates the CDN edge rather than your client — first test with DNS pointing directly at the origin.
6. GeoIP estimates the country of an IP prefix, not a person or a precise physical location.

## Privacy & security

- Captures only IP-level metadata; never cookies, authorization headers, or request bodies.
- Observed IP addresses and derived country data are personal data — rotate or delete `/data/events.jsonl` and keep `.env` at mode `0600`.
- Set a long `DASHBOARD_TOKEN`; raw probes are rate-limited and behave as fixed echoes, not general proxies.
- Only run direct probes against a server you control.

## Documentation

- [`docs/DEPLOYMENT_HANDBOOK.md`](docs/DEPLOYMENT_HANDBOOK.md) — production deployment (DNS, TLS, systemd, nginx, maintenance)
- [`docs/EXPERIMENT_RESULTS.md`](docs/EXPERIMENT_RESULTS.md) — sample leak experiments and findings
- [`docs/SERVER_AGENT_DEPLOYMENT_GUIDE_CN.md`](docs/SERVER_AGENT_DEPLOYMENT_GUIDE_CN.md) — server agent deployment guide (中文)
- [`USAGE.md`](USAGE.md) — usage guide (中文)

## License

[to be decided]
