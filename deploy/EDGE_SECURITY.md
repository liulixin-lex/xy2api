# Edge and HTTP Ingress Security

XY2API supports long-lived SSE and WebSocket requests. Protect the request
ingress without imposing a response `WriteTimeout`: a write deadline would
terminate healthy long generations and streams.

## Application defaults

- `server.max_header_bytes: 65536` limits HTTP/1 request headers to 64 KiB;
  Go maps it to the corresponding HTTP/2 header-list limit.
- `server.read_header_timeout: 10` bounds slow-header attacks. It does not
  limit request processing or response streaming.
- `server.max_request_body_size: 268435456` is the absolute 256 MiB safety net.
- `gateway.max_body_size: 268435456` remains available to multimodal, Gemini,
  image, video, and batch-image endpoints.
- `gateway.text_max_body_size: 33554432` limits the known pure-text
  `/embeddings` and `/alpha/search` endpoints to 32 MiB.
- H2C defaults to 50 concurrent streams per connection, a 2 MiB connection
  upload window, and a 512 KiB stream upload window.
- Invalid credential abuse is limited in process by trusted client IP (IPv6
  `/64`): 120 failures per 60 seconds followed by a 60-second block. This is a
  per-instance safety net; multi-instance enforcement still belongs at the
  load balancer, CDN, or WAF.

Do not add a single application-wide request semaphore: an SSE request may
legitimately occupy it for many minutes. Apply connection and unauthenticated
request controls at the edge; authenticated user/API-key concurrency remains
the application's responsibility.

## Trusted client IPs

`security.trust_forwarded_ip_for_api_key_acl` is enabled by default for upgrade
compatibility. While enabled, raw forwarding headers take over client-IP
resolution for logs and security-sensitive paths. Custom headers from
`security.forwarded_client_ip_headers` are checked in configured order before
the built-in `CF-Connecting-IP`, `X-Real-IP`, and `X-Forwarded-For` fallback.
Header names are case-insensitive, normalized when loaded, de-duplicated, and
limited to 16 unique valid HTTP field names. Header values must contain IP
literals; comma-separated values are supported, invalid entries are skipped,
and public addresses are preferred over private fallback addresses.

The list can be supplied in YAML or with the comma-separated environment
variable `SECURITY_FORWARDED_CLIENT_IP_HEADERS`; an explicitly empty environment
value clears YAML values. It is also editable from the admin security settings
and updates at runtime without a restart. A request snapshots the switch and
header list together, so one request cannot mix old and new settings. Custom
headers are ignored completely when the switch is disabled. In that mode Gin's
`server.trusted_proxies` chain is authoritative: configure only the exact
CIDR/IP addresses that connect directly to XY2API. An explicit empty list
trusts no forwarded client IPs.

On the first upgrade to this mode, a legacy `false` value is changed to `true`
only when `server.trusted_proxies` was not explicitly configured; explicit
proxy policies remain in secure mode. New installations persist the configured
custom header list during database initialization. Existing installations
backfill a missing database value from the YAML configuration. A hidden
migration marker prevents later administrator changes from being overwritten.
If settings cannot be read or the persisted custom-header list is malformed,
the process fails closed to trusted-proxy mode with no custom headers. If a
migration write fails, the computed mode remains active for the current process
and startup records a warning.

Compatibility takeover accepts forwarded headers without validating the direct
peer, including any configured custom header. Protect the origin from direct
access while it is enabled. A CDN deployment must firewall the origin so only
the CDN or load balancer can reach it, and that proxy must overwrite every
trusted client-IP header rather than append an untrusted client value.

Example for a proxy on the same host:

```yaml
server:
  trusted_proxies:
    - 127.0.0.1/32
    - ::1/128
```

## Nginx baseline

Define shared zones in the `http` block. Tune rates to measured legitimate
traffic; the values below are conservative starting points, not universal
capacity targets.

```nginx
limit_conn_zone $binary_remote_addr zone=xy2api_conn:20m;
limit_req_zone  $binary_remote_addr zone=xy2api_auth:20m rate=5r/s;
limit_req_zone  $binary_remote_addr zone=xy2api_api:40m rate=30r/s;
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 443 ssl http2;
    server_name api.example.com;

    client_header_timeout 10s;
    client_max_body_size 256m;
    large_client_header_buffers 4 16k;
    limit_conn xy2api_conn 40;

    location ~ ^/(auth|api/auth)/ {
        limit_req zone=xy2api_auth burst=10 nodelay;
        proxy_pass http://127.0.0.1:8080;
    }

    location ~ ^/(v1/)?(embeddings|alpha/search)$ {
        client_max_body_size 32m;
        limit_req zone=xy2api_api burst=60 nodelay;
        proxy_pass http://127.0.0.1:8080;
    }

    location / {
        limit_req zone=xy2api_api burst=60 nodelay;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_buffering off;
        proxy_request_buffering off;
        proxy_read_timeout 1800s;
        proxy_send_timeout 1800s;
        proxy_pass http://127.0.0.1:8080;
    }
}
```

If Nginx gzip is enabled in the `http` block, keep `text/event-stream` out of
`gzip_types` and do not use `gzip_types *` for XY2API. The
`proxy_buffering off` setting above prevents proxy buffering, but it does not
disable the gzip response filter. Use an explicit list for ordinary responses:

```nginx
gzip on;
gzip_types text/plain text/css application/json application/javascript application/xml image/svg+xml;
```

If a shared global configuration cannot exclude SSE by content type, set
`gzip off;` in the locations serving streaming API routes. This leaves gzip
available for the web UI and static assets.

Do not use an incoming `$http_x_forwarded_for` value unless Nginx real-IP
processing is restricted to explicit trusted proxy CIDRs.

### Behind a layer-4 SNI router

When port 443 is first handled by an Nginx `stream` block that routes by SNI
(`ssl_preread`) and forwards TCP to the HTTP server, `$remote_addr` in the
HTTP server is the loopback address of that hop. XY2API then records every
client as `127.0.0.1`, and IP rate limits, IP allow lists, registration
abuse checks and audit logs stop working. Carry the client address with the
PROXY protocol on the hop that reaches the XY2API site:

```nginx
stream {
    map $ssl_preread_server_name $sni_backend {
        api.example.com 127.0.0.1:9446;   # the XY2API site below
        default         127.0.0.1:8443;
    }
    server {
        listen 443;
        ssl_preread on;
        proxy_protocol on;                # only if every backend accepts it
        proxy_pass $sni_backend;
    }
}

http {
    server {
        listen 127.0.0.1:9446 ssl http2 proxy_protocol;
        server_name api.example.com;
        set_real_ip_from 127.0.0.1;
        real_ip_header proxy_protocol;
        # ... location / as above, with
        # proxy_set_header X-Real-IP $proxy_protocol_addr;
        # proxy_set_header X-Forwarded-For $proxy_protocol_addr;
    }
}
```

`proxy_protocol on` applies to every backend of that `stream` server. When
other backends cannot read the PROXY header, give the XY2API site its own
listener that receives the header from the first hop, instead of a second
relay that drops it. Check the result with an external request and the
`client_ip` field of the XY2API access log.

### Health checks

`/health` answers 200 without touching any dependency; keep it for container
health checks. `/ready` (0.4.2) checks the database and Redis (2 s each) and
answers 503 with the names of the failed checks; use it for proxy health
checks (for example Caddy `health_uri /ready`) and external monitoring.

## Caddy and CDN

The bundled `deploy/Caddyfile` sets a 64 KiB header limit, a 10-second header
timeout, a 256 MiB absolute body limit, and overwrites forwarded addresses from
the TCP peer. It is therefore a direct-to-Caddy baseline. Do not use its
`{remote_host}` forwarding lines unchanged behind a CDN: all clients would be
attributed to a CDN egress address, collapsing rejection aggregation and the
invalid-auth limiter onto unrelated users.

The bundled Caddy configuration leaves `flush_interval` unset so Caddy can
automatically flush `text/event-stream` responses while still propagating
client cancellation upstream. Do not set it globally: positive values can add
streaming latency, while Caddy 2.6.2's special `-1` mode also causes
reverse-proxied requests to continue after clients disconnect. The
configuration uses an explicit response content-type list for compression. Do
not replace that list with `text/*` or the shorthand `encode gzip zstd`: both
match `text/event-stream` and can buffer SSE until the response ends. Keep
streaming responses uncompressed while retaining compression for the web UI,
JSON, and static assets.

For a CDN deployment, first firewall the origin so only current CDN egress
CIDRs can connect. Then configure those exact ranges as Caddy trusted proxies
and derive upstream headers from Caddy's parsed `{client_ip}`. For example:

```caddyfile
{
	servers {
		trusted_proxies static 192.0.2.0/24 2001:db8:1234::/48
		trusted_proxies_strict
		client_ip_headers CF-Connecting-IP X-Forwarded-For
	}
}

api.example.com {
	reverse_proxy 127.0.0.1:8080 {
		header_up X-Real-IP {client_ip}
		header_up X-Forwarded-For {client_ip}
	}
}
```

Replace the documentation ranges with the CDN's published, automatically
maintained egress ranges. `CF-Connecting-IP` is safe here only because direct
origin access is blocked and Caddy trusts only those TCP peers. Configure
XY2API `server.trusted_proxies` with the Caddy address/private subnet so the
application accepts only Caddy's rewritten headers.

### Edge marker for streams behind a CDN (optional since 0.4.1)

XY2API sends early heartbeats on a slow streaming request only when it knows
the request came through a CDN; otherwise a stream whose first token takes
longer than the CDN's idle timeout (about 100–125 seconds on Cloudflare) is
cut before the client receives a byte.

Since 0.4.1 XY2API recognises Cloudflare and Alibaba Cloud ESA by the headers
they add to every origin request: `CF-Ray` or `CDN-Loop: cloudflare` is `cf`,
`CDN-Loop: esa` is `esa`. No reverse proxy marker and no
`server.trusted_proxies` entry is needed for this. Forged headers only change
how the forger's own request is answered (early SSE headers and heartbeats,
in-stream terminal frames, 503 instead of 502/504); authentication, billing
and other users are not affected. Removing `cf` or `esa` from the gateway
resilience setting `keepalive.edges` turns the matching detection off.
XY2API never trusts a proxy automatically: behind Docker the peer is the
bridge gateway, which a published port shares with direct clients.

For another CDN, or a CDN that does not send these headers, the reverse proxy
can still mark the request with `X-XY-Edge`. Set the marker from the TCP peer
address only, and drop any client-supplied value first:

```caddyfile
(xy_edge_marker) {
	request_header -X-XY-Edge
	@xy_edge_cf remote_ip <Cloudflare egress ranges>
	request_header @xy_edge_cf X-XY-Edge cf
	@xy_edge_esa remote_ip <ESA egress ranges>
	request_header @xy_edge_esa X-XY-Edge esa
}
```

XY2API reads the marker only when the TCP peer is in an explicitly configured
`server.trusted_proxies` list (`SERVER_TRUSTED_PROXIES`) and the value is
listed in the gateway resilience setting `keepalive.edges` (default `cf` and
`esa`); a valid marker takes precedence over the CDN headers. Add any new
edge name to `keepalive.edges`.

Cloudflare also replaces an origin 502 or 504 with its own plain-text page,
dropping the JSON error, `Retry-After` and the request ID. XY2API answers
requests that came through a CDN with 503 instead and keeps the original status in
`X-XY-Upstream-Status`; let the proxy's own error pages for API paths return a
JSON 503 with `Retry-After` too. The complete configuration is in
[`docs/GATEWAY_RESILIENCE.md`](../docs/GATEWAY_RESILIENCE.md) §2 and the
[0.4.0 runbook](../openspec/changes/gateway-resilience-20261015/runbook.md) §3.

Caddy core does not provide a general request-rate limiter; use a trusted
CDN/WAF, a supported rate-limit module, or host firewall controls.

At a CDN/WAF, configure connection limits, header/body limits, bot challenges,
and per-IP/ASN rates before traffic reaches the origin. Allow origin ingress
only from CDN egress CIDRs or a private load balancer. Keep the application port
off the public Internet.

## DDoS boundary

Application checks reduce amplification after a connection reaches Go. They
cannot absorb volumetric attacks, TLS floods, bandwidth saturation, or a large
distributed source set. Those require upstream network capacity, CDN/WAF
filtering, provider firewall rules, and origin isolation. Avoid high-cardinality
metrics or per-request database security logs during rejection storms.
