# stock-mcp(Go 版)

以 **MCP Streamable HTTP transport** 提供服務的唯讀 MCP Server。預設透過 `stock_rust` 的版本化 Data API 查詢台股資料；`db` 模式只供遷移期比對。

> **非即時行情聲明**:本服務所有報價均為「資料庫中目前最新可取得的日報價或歷史日線資料」,**非交易所逐筆或保證即時行情**,僅供資訊參考。所有價格輸出都帶有 `is_realtime: false` 與免責聲明欄位。

本專案是 `docs/stock-mcp-project-prompt.md` 規格的第三個實作(前兩個為 TypeScript 版 `stock_ts` 與 Rust 版 `stock_mcp_rust`),使用 **Go 1.27** 與官方 [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk)。

## 系統架構

```text
                    ┌───────────────────────────────────────────────┐
                    │                  stock-mcp(Go)               │
 AI client          │                                               │
 (MCP over ─ HTTPS ─▶ 反向代理 ─▶ web/(HTTP 層)                    │
  Streamable HTTP)  │       GET /healthz(免驗證)                  │
                    │       {MCP_PATH}:                             │
                    │         1. API key 驗證(401)                │
                    │         2. rate limit(429)                  │
                    │         3. MCP StreamableHTTPHandler(SDK)   │
                    │              │                                │
                    │       stock/(領域層)                        │
                    │         tools.go   ── 4 個 MCP tool           │
                    │         repository ── 參數化 SQL(唯讀)      │
                    │              │                                │
                    └──────────────┼────────────────────────────────┘
                                   ▼
                  stock_rust Data API (Bearer key)
                               │
                           PostgreSQL
```

- **`stock/`**:資料模型、Data API client、遷移期的 PostgreSQL repository、MCP tool 實作。
- **`web/`**:API key 驗證、rate limit、健康檢查、HTTP server。tool 與資料庫邏輯完全與 transport 解耦,未來可新增 stdio adapter。
- **`config/`**:環境變數載入與啟動驗證。

## 系統需求

- Go 1.27(開發當下為 `go1.27rc2`,與 `go.mod`/`Dockerfile` 一致)
- 可連線的 PostgreSQL(既有 `stock_rust` 資料庫)
- 正式環境:HTTPS 反向代理(Caddy / Nginx)

## 安裝與設定

```bash
git clone <本專案>
cd stock_mcp_go
cp .env.example .env   # 填入 STOCK_RUST_API_* 與 MCP_API_KEY
go build ./...
```

API 模式啟動時會驗證 `STOCK_RUST_API_BASE_URL`、`STOCK_RUST_API_KEY` 與 `MCP_API_KEY`；db 模式才需要 `DATABASE_URL`。

### 環境變數

| 變數 | 預設 | 說明 |
|---|---|---|
| `APP_ENV` | `development` | `development` / `production` / `test` |
| `HOST` | `127.0.0.1` | 綁定位址(容器內請設 `0.0.0.0`) |
| `PORT` | `3000` | 監聽 port |
| `MCP_PATH` | `/mcp` | MCP endpoint 路徑 |
| `TRUST_PROXY` | `false` | 僅在 `true` 時信任 `X-Forwarded-For` |
| `DATA_SOURCE` | `api` | `api`（正式）或 `db`（遷移期比對） |
| `STOCK_RUST_API_BASE_URL` | api 模式必填 | 例如 `http://127.0.0.1:9002` |
| `STOCK_RUST_API_KEY` | api 模式必填 | stock_rust 的 `DATA_API_KEY`，不可與 MCP key 共用 |
| `API_TIMEOUT_MS` | `5000` | Data API HTTP timeout |
| `DATABASE_URL` | db 模式必填 | 僅遷移期的唯讀連線字串 |
| `DB_POOL_MAX` | `10` | 連線池上限 |
| `DB_CONNECTION_TIMEOUT_MS` | `5000` | 連線逾時 |
| `DB_STATEMENT_TIMEOUT_MS` | `5000` | 每條查詢的 statement timeout |
| `MCP_API_KEY` | (必填) | Bearer API key |
| `RATE_LIMIT_WINDOW_MS` | `60000` | rate limit 視窗 |
| `RATE_LIMIT_MAX_REQUESTS` | `60` | 視窗內每個 API key + IP 的請求上限 |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

## 建立唯讀資料庫帳號

本服務**只讀取**資料庫,請務必使用獨立、最小權限的唯讀帳號(概念範例,請替換資料庫名稱與強密碼):

```sql
CREATE ROLE stock_mcp_reader LOGIN PASSWORD '請替換為強密碼';
GRANT CONNECT ON DATABASE stock TO stock_mcp_reader;
GRANT USAGE ON SCHEMA public TO stock_mcp_reader;
GRANT SELECT ON TABLE
  public.stocks,
  public.last_daily_quotes,
  public."DailyQuotes",
  public.quote_history_record
TO stock_mcp_reader;
```

**不可**授予 `INSERT`、`UPDATE`、`DELETE`、`TRUNCATE`、`CREATE`、`ALTER`、`DROP` 或任意 function 的 `EXECUTE` 權限。

## 啟動

```bash
# 本機開發(自動載入 .env)
make dev            # 或 go run .

# 編譯後執行
make start

# Docker
docker compose -f docker-compose.example.yml up --build
```

健康檢查(不需 API key):

```bash
curl http://127.0.0.1:3000/healthz
# {"status":"ok"}
```

## MCP endpoint 與驗證

- `POST /mcp`:MCP JSON-RPC 請求
- `GET /mcp`:Streamable HTTP 的 SSE 串流/可恢復連線(由官方 SDK 處理)
- `DELETE /mcp`:終止 MCP session

所有 MCP 請求都必須帶:

```http
Authorization: Bearer <MCP_API_KEY>
```

驗證失敗回傳 HTTP 401 且不會查詢資料庫;超過 rate limit 回傳 HTTP 429。

MCP client 設定範例(以 Claude Code 為例):

```bash
claude mcp add --transport http stock-mcp https://your-domain.example/mcp \
  --header "Authorization: Bearer <MCP_API_KEY>"
```

## MCP tools

所有 tool 回傳一段繁體中文文字摘要與 `structuredContent`;價格資料一律包含 `data_kind`、`data_as_of`、`is_realtime: false`、`disclaimer`。

### `search_stock`

以代號或名稱關鍵字搜尋(代號完全符合優先)。查無資料回傳空陣列,不是錯誤。

```json
{"name": "search_stock", "arguments": {"query": "台積電", "limit": 10}}
```

### `get_latest_daily_quote`

查詢最新一筆日報價。股票存在但沒有報價時 `quote` 為 `null`;股票不存在回傳 tool error「找不到股票代號:{symbol}」。`data_as_of` 依序取 `updated_time` → `record_time` → `date`。

```json
{"name": "get_latest_daily_quote", "arguments": {"symbol": "2330"}}
```

### `get_price_history`

歷史日線,按日期新到舊排序;`from` 不可晚於 `to`,`limit` 預設 30(1–365)。

```json
{"name": "get_price_history", "arguments": {"symbol": "2330", "from": "2026-06-01", "to": "2026-07-13", "limit": 30}}
```

### `get_stock_profile`

基本資料、最新報價、近一季/近四季 EPS、每股淨值、ROE、權值、發行股數與歷史高低點。缺失資料一律回傳 `null`,不以 0 或猜測值取代。

```json
{"name": "get_stock_profile", "arguments": {"symbol": "2330"}}
```

> **關聯假設**:`quote_history_record.security_code` 與 `stocks."SecurityCode"` 為等價關聯(沿用原始規格書明確指定的假設,非本專案自行推測)。

### `get_realtime_snapshot`

僅在 `DATA_SOURCE=api` 出現。資料來自第三方站點採集的快照，`is_realtime` 固定為 `false`，並以 `updated_at` 作為 `data_as_of`；非交易時段或個股無快照時會回 tool error，建議改查 `get_latest_daily_quote`。

## 安全設計

- 只讀取資料庫;SQL 全部集中在 `stock/repository.go`,一律參數化查詢($1、$2…),禁止字串拼接。
- API key 以常數時間比較(`crypto/subtle`),防 timing attack。
- rate limit 以「API key 雜湊 + 來源 IP」計數;預設 60 次/分鐘,可由環境變數調整。
- 每條查詢套用 `statement_timeout`(預設 5 秒);歷史查詢皆有最大筆數限制。
- 錯誤回應與 log 不包含資料庫主機、帳密、SQL 原文、堆疊或 Authorization header。
- log 為結構化 JSON(`log/slog`),不記錄任何敏感資訊。

## HTTPS 部署(反向代理)

正式環境**必須**使用 HTTPS,本服務假設放在反向代理後方、只綁定 `127.0.0.1`。

Caddy 範例(自動簽發憑證):

```caddyfile
mcp.example.com {
    reverse_proxy 127.0.0.1:3000
}
```

Nginx 片段:

```nginx
location /mcp {
    proxy_pass http://127.0.0.1:3000;
    proxy_http_version 1.1;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_buffering off;   # SSE 串流必須關閉緩衝
    proxy_read_timeout 3600s;
}
```

放在代理後方時將 `TRUST_PROXY=true`,rate limit 才會使用真實用戶端 IP。

## 測試

```bash
make test        # 單元測試(不需要資料庫)
make lint        # go vet
make fmt-check   # gofmt 檢查
```

整合測試需明確設定 `TEST_DATABASE_URL` 才會執行,未設定時自動跳過——預設不要求連線真實資料庫:

```bash
TEST_DATABASE_URL=postgresql://... go test ./stock/ -run TestRepositoryIntegration -v
```

## 已知限制

- **資料新鮮度取決於 `stock_rust` 專案寫入資料庫的頻率**;本服務只是資料庫的唯讀視圖,不主動抓取任何交易所資料。
- rate limiter 為單機記憶體實作;水平擴充多實體時需改用共享儲存(如 Redis)。
- 第一版資料來源僅限既有 PostgreSQL,無任何即時行情來源。
