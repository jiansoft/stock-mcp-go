# stock-mcp-go

[English](README.md) | [繁體中文](README.zh-TW.md)

A read-only Model Context Protocol (MCP) server for Taiwan stock data, built with Go and the official MCP Go SDK. It exposes stock quotes, company fundamentals, financial history, valuation, screening, and market analytics over stateless Streamable HTTP.

> [!IMPORTANT]
> Most data returned by this server is historical or the latest data available from the upstream database. It is not exchange-grade tick data and must not be treated as guaranteed real-time market data. The only tool that may return real-time results is `get_market_movers` during market hours. All responses include data provenance, freshness, and disclaimer fields.

## Features

- Stateless MCP Streamable HTTP endpoint with read-only tool annotations
- 19 read-only tools backed by the `stock_rust` Data API
- Traditional Chinese text summaries plus structured content for programmatic use
- Multiple MCP API keys with create, edit, enable, disable, rotate, and revoke workflows
- HMAC-SHA-256 API-key verification with a server-side pepper; plaintext keys are never stored
- Embedded administration UI with secure, eight-hour HttpOnly sessions
- Per-key and per-client-IP rate limiting
- Origin validation, request-size limits, structured logging, and graceful shutdown
- Liveness and data-source-aware readiness endpoints
- One structured log entry per tool call (tool, result, duration, API-key prefix)
- Embedded Swagger UI (`/docs`) and an OpenAPI 3.1 document (`/openapi.json`) generated from the registered tools
- Distroless, non-root container image with a built-in health check

## Architecture

```text
MCP client
    │  HTTPS + Authorization: Bearer <MCP_API_KEY>
    ▼
Reverse proxy
    ▼
stock-mcp-go
    ├── /mcp                         stateless MCP endpoint
    ├── /healthz and /readyz         health endpoints
    ├── /admin/mcp-api-keys          embedded administration UI
    ├── web/                          auth, rate limiting, HTTP security
    ├── apikey/                       SQLite key store and in-memory snapshot
    └── stock/                        MCP tools and Data API client
          └── HTTP ─────────────────► stock_rust Data API
```

> [!NOTE]
> Direct PostgreSQL access (`db` mode) was removed in October 2026; the `stock_rust` Data API is the only data source. `DATA_SOURCE=db` is rejected at startup, while leftover `DATABASE_URL` and `DB_*` variables are ignored and can be deleted.

## MCP tools

| Tool | Availability | Purpose |
| --- | --- | --- |
| `search_stock` | API | Search by stock symbol or Chinese/English name |
| `get_latest_daily_quote` | API | Get the latest available daily quote |
| `get_price_history` | API | Query historical daily prices by date range |
| `get_stock_profile` | API | Get company details, quote, EPS, ROE, book value, and price extremes |
| `get_realtime_snapshot` | API | Get a third-party near-real-time snapshot; not guaranteed real-time |
| `get_monthly_revenue_history` | API | Query monthly and cumulative revenue history |
| `get_financial_statement_history` | API | Query quarterly or annual financial metrics |
| `get_dividend_history` | API | Query historical cash and stock dividends |
| `get_stock_valuation` | API | Get the latest or date-relative valuation model result |
| `get_market_breadth` | API | Get advances/declines, moving-average breadth, and valuation distribution |
| `get_dividend_yield_ranking` | API | Rank TWSE/TPEX stocks by historical dividend yield |
| `screen_stocks` | API | Screen stocks with an allowlisted set of fundamental and valuation filters |
| `get_market_index_history` | API | Query TAIEX index history |
| `get_dividend_calendar` | API | Query ex-dividend, ex-rights, and dividend-payment events |
| `get_qfii_holding_ranking` | API | Rank the latest QFII holding snapshot |
| `get_market_movers` | API | Rank daily gainers, losers, or volume; automatically selects intraday or closing data |
| `get_chip_data` | API | Chip data for one stock: daily institutional flows and margin balances, buy/sell streaks, major holders, insider pledges, and main broker flow |
| `get_cagr_ranking` | API | Rank stocks by annualized return over M3-Y10 (price only, with dividends, or reinvested), filterable by market and industry |
| `get_stock_cagr` | API | Annualized and total return for one stock over every period |

Every tool is read-only. Outputs include `data_kind`, `data_as_of`, `is_realtime`, and a disclaimer. Missing values remain `null`; the server does not invent zeroes or estimates.

## Requirements

- Go 1.27 (`go.mod` currently targets `go1.27rc2`)
- Default `api` mode: access to a compatible `stock_rust` Data API and its bearer key
- Production: an HTTPS reverse proxy such as Caddy or Nginx

## Quick start

```bash
git clone https://github.com/<owner>/stock-mcp-go.git
cd stock-mcp-go
cp .env.example .env
```

At minimum, configure these values:

```dotenv
STOCK_RUST_API_BASE_URL=http://127.0.0.1:9002
STOCK_RUST_API_KEY=replace-with-a-dedicated-upstream-key
MCP_API_KEY=replace-with-an-initial-client-key
MCP_API_KEY_PEPPER=replace-with-at-least-32-random-bytes
MCP_ADMIN_TOKEN=replace-with-a-different-32-byte-random-secret
```

`MCP_API_KEY` is only a bootstrap value. It is imported when the API-key SQLite database is empty and can be removed from the environment after the first successful import.

Start the server:

```bash
go run .
```

The checked-in `.env.example` uses port `9005`; the application default is `3000` when `PORT` is unset.

Verify it:

```bash
curl http://127.0.0.1:9005/healthz
curl http://127.0.0.1:9005/readyz
```

Expected responses are `{"status":"ok"}`. `/readyz` returns HTTP 503 when the upstream Data API is unavailable or rejects the key.

API documentation: open `http://127.0.0.1:9005/docs` (Swagger UI, no authentication). It is generated from the registered tools, so `tools/call` lists each tool's arguments; Try it out works with an MCP API key and adds the `Accept: application/json, text/event-stream` header that MCP requires.

## MCP client configuration

The endpoint accepts authenticated `POST` requests at `/mcp` by default:

```http
Authorization: Bearer <MCP_API_KEY>
```

Claude Code example:

```bash
claude mcp add --transport http stock-mcp https://mcp.example.com/mcp \
  --header "Authorization: Bearer <MCP_API_KEY>"
```

The server uses stateless Streamable HTTP. It does not provide stdio transport, server-side sessions, or an SSE subscription endpoint; `GET /mcp` and `DELETE /mcp` return `405 Method Not Allowed`.

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `APP_ENV` | `development` | `development`, `production`, or `test` |
| `HOST` | `127.0.0.1` | Listen address; use `0.0.0.0` inside a container |
| `PORT` | `3000` | HTTP port (`.env.example` overrides it to `9005`) |
| `MCP_PATH` | `/mcp` | MCP endpoint path |
| `TRUST_PROXY` | `false` | Trust proxy-appended client IP information |
| `TRUSTED_PROXY_HOPS` | `1` | Number of trusted proxies that append `X-Forwarded-For` |
| `STOCK_RUST_API_BASE_URL` | required | Upstream Data API base URL |
| `STOCK_RUST_API_KEY` | required | Dedicated upstream bearer key |
| `API_TIMEOUT_MS` | `5000` | Upstream HTTP timeout |
| `MCP_API_KEY` | empty | One-time compatibility bootstrap key |
| `MCP_API_KEY_DB_PATH` | `data/mcp-api-keys.db` | SQLite API-key database path |
| `MCP_API_KEY_PEPPER` | required | HMAC pepper of at least 32 bytes |
| `MCP_ADMIN_TOKEN` | required | Separate administration secret of at least 32 bytes |
| `MCP_TRUSTED_ORIGINS` | empty | Comma-separated allowed browser origins |
| `RATE_LIMIT_WINDOW_MS` | `60000` | Rate-limit window |
| `RATE_LIMIT_MAX_REQUESTS` | `60` | Requests per API key and source IP per window |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |

The upstream key, MCP client keys, pepper, and admin token must all be separate secrets. Do not commit `.env`.

Tool-call log: every `tools/call` writes one `msg="MCP 工具呼叫"` entry with `tool`, `status` (`ok`, `tool_error`, `protocol_error`), `duration_ms`, and the caller's `key_prefix`; arguments are not logged. Use `LOG_LEVEL=info` in production.

## API-key administration

Open the embedded UI at:

```text
https://mcp.example.com/admin/mcp-api-keys
```

Sign in once with `MCP_ADMIN_TOKEN`. The server exchanges it for an in-memory, eight-hour `HttpOnly`, `Secure`, `SameSite=Strict` session cookie. The token is not stored in browser storage.

The administration API also accepts `Authorization: Bearer <MCP_ADMIN_TOKEN>` for automation:

| Method | Endpoint | Action |
| --- | --- | --- |
| `POST` / `DELETE` | `/api/admin/session` | Sign in or sign out |
| `GET` / `POST` | `/api/admin/mcp-api-keys` | List or create keys |
| `GET` / `PATCH` / `DELETE` | `/api/admin/mcp-api-keys/{id}` | Read, update, or revoke a key |
| `POST` | `/api/admin/mcp-api-keys/{id}/enable` | Enable a key |
| `POST` | `/api/admin/mcp-api-keys/{id}/disable` | Disable a key |
| `POST` | `/api/admin/mcp-api-keys/{id}/rotate` | Rotate a key |

Complete API keys are returned only once, after creation or rotation. SQLite stores a public prefix and `HMAC-SHA-256(pepper, full-key)`, never the plaintext key. Changes are published to an immutable in-memory snapshot immediately after the database transaction commits.

The API-key store supports one Go process with one local SQLite volume. Do not share the same SQLite file between replicas. A multi-replica deployment requires a shared relational key store and a cross-process refresh mechanism.

## Docker

Build and run from source:

```bash
docker compose -f docker-compose.example.yml up --build
```

The Compose example publishes `127.0.0.1:9004` to container port `3000` and persists API-key state in the `mcp-api-key-data` named volume. The image runs as a non-root user and uses the executable's `-health-check` mode because the distroless runtime contains no shell, `curl`, or `wget`.

`Dockerfile_live` and `control.sh` provide a separate ARM deployment flow for binaries produced by `build.ps1`. They support Linux ARM64 and ARMv7 and expect the selected `stock-mcp_linux_*` binary at the deployment root.

### Deploying to a Raspberry Pi (native process)

```powershell
.\scripts\deploy-armv7.ps1 -Build        # cross-compile, upload, control.sh update, verify
.\scripts\deploy-armv7.ps1 -StageOnly    # upload to /tmp and verify sha256 only
```

The script checks the ELF architecture and the uploaded sha256, then verifies the process, port, `/healthz`, `/readyz`, and the startup log, printing rollback commands on failure (`control.sh` keeps the previous binary as `<binary>.<timestamp>`). Defaults: `pi@192.168.111.138:/opt/stock_mcp`, port `9005`.

## Reverse proxy

Production traffic must use HTTPS. A minimal Caddy configuration is:

```caddyfile
mcp.example.com {
    reverse_proxy 127.0.0.1:9005
}
```

Minimal Nginx configuration:

```nginx
location / {
    proxy_pass http://127.0.0.1:9005;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
}
```

Set `TRUST_PROXY=true` only when the service is actually behind a trusted proxy. `TRUSTED_PROXY_HOPS` must match the number of proxies that append `X-Forwarded-For`: use `1` for client → Nginx → server, or `2` for client → CDN → Nginx → server. A value that is too large can make the rate-limit IP identity spoofable.

Restrict the administration routes at the network edge when possible:

- `/admin/`
- `/api/admin/`

## Data and security semantics

- `get_market_movers` is the only tool whose `is_realtime` value can be `true`.
- Between the Taiwan market close and availability of the final daily data, movers may fall back to the previous trading day; the response reports its actual source and date.
- `get_realtime_snapshot` is a third-party near-real-time snapshot but is deliberately marked as not guaranteed real-time.
- Valuation bands, rankings, and screening results describe historical model output and are not targets or trading recommendations.
- QFII holdings are a latest snapshot, not a time series; the tool cannot infer accumulation or reduction trends.
- The MCP endpoint has a 1 MiB request-body limit; administration requests have a 64 KiB limit.
- Authentication failures are separately rate-limited before normal per-key request limiting.
- Secrets and authorization headers are excluded from logs.

## Development

```bash
make test        # go test ./...
make lint        # go vet ./...
make fmt-check   # verify gofmt formatting
make build       # build ./stock-mcp
```

## Known limitations

- Data freshness depends on the upstream `stock_rust` collection and processing schedule.
- Rate-limit counters and API-key verification snapshots are process-local.
- The SQLite API-key repository is designed for a single process and local persistent volume.
- The server exposes MCP tools only; it does not expose Resources, Prompts, or Sampling.
- All output is informational and does not constitute investment advice.
