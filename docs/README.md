# Proxy Leak Lab

A self-hosted lab for comparing what your server sees through browser HTTP/WebRTC and through a deliberately proxy-aware vs proxy-unaware local client.

## What the first version tests

- Browser HTTPS request source IP and selected forwarding headers
- Browser IPv4-only and IPv6-only HTTPS paths
- Browser WebRTC/STUN server-reflexive candidates
- HTTP/1.1, HTTP/2, and HTTP/3 at the Caddy edge
- Raw TCP source IP
- Raw UDP source IP
- Direct STUN/XOR-MAPPED source IP
- HTTP through `HTTP_PROXY` / `HTTPS_PROXY`
- HTTP and raw TCP through an explicit SOCKS5 proxy

It intentionally does **not** capture cookies, authorization headers, or request bodies.

## DNS records

Create these records before startup. Do not place a CDN or reverse proxy in front of them for the first test.

| Name | Type | Value |
|---|---|---|
| `leak.example.com` | A | server IPv4 |
| `leak.example.com` | AAAA | server IPv6, if available |
| `v4.example.com` | A | server IPv4 |
| `v6.example.com` | AAAA | server IPv6 |
| `stun.example.com` | A | server IPv4 |
| `stun.example.com` | AAAA | server IPv6, if available |

If the server has no public IPv6, omit the AAAA records; IPv6 tests will correctly fail rather than report an address.

## Server prerequisites

- Ubuntu or another Linux server
- Docker Engine with Compose plugin
- Linux host networking (the Compose file uses `network_mode: host` so TCP/UDP peer addresses and IPv6 are not obscured by a container bridge)
- Public ports:
  - TCP 80, 443, 9001
  - UDP 443, 3478, 9002
- The DNS records above pointed directly at the server

Example UFW rules:

```bash
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw allow 443/udp
sudo ufw allow 9001/tcp
sudo ufw allow 9002/udp
sudo ufw allow 3478/udp
```

## Start the server

```bash
cp .env.example .env
nano .env
docker compose build
docker compose up -d
docker compose logs -f
```

The app binds its HTTP backend only to `127.0.0.1:8080`; raw TCP/UDP/STUN listeners bind publicly. Caddy obtains public TLS certificates automatically when DNS points to the server and TCP 80/443 are reachable.

Open:

```text
https://leak.example.com
```

Paste the `DASHBOARD_TOKEN` into the page and run the browser tests.

## Build the local client

On any machine with Go 1.23 or newer:

```bash
go build -o leak-client ./client
```

Windows PowerShell:

```powershell
$env:HTTPS_PROXY = "http://127.0.0.1:7890"   # optional; Go reads proxy environment variables
.\leak-client.exe -domain example.com
```

An explicit HTTP proxy is preferable on Windows because this client does not automatically read the Windows GUI proxy setting:

```powershell
.\leak-client.exe -domain example.com -http-proxy http://127.0.0.1:7890
```

Explicit SOCKS5:

```powershell
.\leak-client.exe -domain example.com -socks5 socks5://127.0.0.1:1080
```

Avoid intentional direct probes:

```powershell
.\leak-client.exe -domain example.com -socks5 socks5://127.0.0.1:1080 -skip-direct
```

The client prints one JSON report. Copy its `test_id` into the dashboard field, or open `https://leak.example.com/?test_id=THE_ID`, to view browser and native events under one identifier.

## How to read the two path classes

- `proxy-aware`: the program explicitly uses `HTTP_PROXY` / `HTTPS_PROXY` or the configured SOCKS5 proxy.
- `proxy-unaware`: the program opens ordinary TCP/UDP sockets. A system-wide TUN/VPN should still route these through the tunnel. An application-only HTTP/SOCKS proxy normally will not.

Seeing your normal ISP IP in `proxy-unaware` tests is expected when you only configured an application proxy. It is a leak only if your intended security claim is that all device traffic should use a full tunnel.

## Check HTTP/3 from a command line

Caddy publishes UDP 443 for HTTP/3. With a curl build that supports HTTP/3:

```bash
curl -v --http3-only "https://leak.example.com/api/http?test_id=manual-h3&label=curl-http3"
```

A failure does not automatically mean a leak; QUIC may be unsupported or blocked. Compare the server's HTTP observation with TCP-based HTTPS.

## Current limitations

1. The STUN server logs source IP and returns XOR-MAPPED-ADDRESS, but browser STUN requests do not carry the dashboard `test_id`; correlate them by timestamp and the ICE candidate shown in the page.
2. SOCKS5 support in the client uses TCP CONNECT. SOCKS5 UDP ASSOCIATE is deliberately not implemented in this first version. The direct UDP test asks whether the operating system/TUN captures UDP that does not know about the proxy.
3. A true DNS-leak test needs an authoritative DNS server for a delegated test subdomain. The system resolver result printed by this client is not enough to identify the recursive resolver path.
4. ICMP and traceroute need operating-system privileges/tools and are not included in the cross-platform client.
5. If you place Cloudflare or another CDN in front of the site, Caddy sees the CDN edge. First test with DNS pointing directly to the origin.

## Security notes

- Set a long `DASHBOARD_TOKEN`.
- Do not expose the event file over HTTP.
- The raw probes are rate-limited and bound to fixed echo behavior; they are not general proxies.
- Rotate or delete `the Docker volume at `/data/events.jsonl`` because IP addresses are personal data.
- This lab intentionally reveals direct-path addresses to a server you control. Do not run direct tests against infrastructure you do not trust.

Export the event log when needed:

```bash
docker compose exec app cat /data/events.jsonl > events.jsonl
```
