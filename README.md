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
  Streamable HTTP)  │       GET /healthz、/readyz(免驗證)         │
                    │       /admin/mcp-api-keys(管理 UI)          │
                    │       /api/admin/mcp-api-keys/*(管理 API)   │
                    │       {MCP_PATH}:                             │
                    │         1. API key 驗證(401)                │
                    │         2. rate limit(429)                  │
                    │         3. MCP StreamableHTTPHandler(SDK)   │
                    │              │                                │
                    │       stock/(領域層)                        │
                    │         tools.go   ── 15 個 MCP tool(api 模式)│
                    │         repository ── 參數化 SQL(唯讀)      │
                    │              │                                │
                    └──────────────┼────────────────────────────────┘
                                   ▼
                  stock_rust Data API (Bearer key)
                               │
                           PostgreSQL
```

- **`stock/`**:資料模型、Data API client、遷移期的 PostgreSQL repository、MCP tool 實作。
- **`apikey/`**:多組 MCP API Key domain、SQLite repository、HMAC 驗證、不可變 snapshot、audit 與 last-used 節流。
- **`web/`**:API key 驗證、管理 REST API／UI、rate limit、健康檢查、HTTP server。tool 與資料庫邏輯完全與 transport 解耦。
- **`config/`**:環境變數載入與啟動驗證。

## 系統需求

- Go 1.27(開發當下為 `go1.27rc2`,與 `go.mod`/`Dockerfile` 一致)
- 可連線的 PostgreSQL(既有 `stock_rust` 資料庫)
- 正式環境:HTTPS 反向代理(Caddy / Nginx)

## 安裝與設定

```bash
git clone <本專案>
cd stock_mcp_go
cp .env.example .env   # 填入 STOCK_RUST_API_*、pepper、admin token 與 bootstrap key
go build ./...
```

API 模式啟動時會驗證 `STOCK_RUST_API_BASE_URL`、`STOCK_RUST_API_KEY`、`MCP_API_KEY_PEPPER` 與 `MCP_ADMIN_TOKEN`；db 模式才需要 `DATABASE_URL`。`MCP_API_KEY` 現在只供首次相容性匯入，不再是每次啟動的必要條件。

### 環境變數

| 變數 | 預設 | 說明 |
|---|---|---|
| `APP_ENV` | `development` | `development` / `production` / `test` |
| `HOST` | `127.0.0.1` | 綁定位址(容器內請設 `0.0.0.0`) |
| `PORT` | `3000` | 監聽 port |
| `MCP_PATH` | `/mcp` | MCP endpoint 路徑 |
| `TRUST_PROXY` | `false` | 僅在 `true` 時參考 `X-Forwarded-For` |
| `TRUSTED_PROXY_HOPS` | `1` | 前方有幾層會自行附加 `X-Forwarded-For` 的受信任代理。**設錯會導致 rate limit 可被繞過**,詳見「反向代理」 |
| `DATA_SOURCE` | `api` | `api`（正式）或 `db`（遷移期比對） |
| `STOCK_RUST_API_BASE_URL` | api 模式必填 | 例如 `http://127.0.0.1:9002` |
| `STOCK_RUST_API_KEY` | api 模式必填 | stock_rust 的 `DATA_API_KEY`，不可與 MCP key 共用 |
| `API_TIMEOUT_MS` | `5000` | Data API HTTP timeout |
| `DATABASE_URL` | db 模式必填 | 僅遷移期的唯讀連線字串 |
| `DB_POOL_MAX` | `10` | 連線池上限 |
| `DB_CONNECTION_TIMEOUT_MS` | `5000` | 連線逾時 |
| `DB_STATEMENT_TIMEOUT_MS` | `5000` | 每條查詢的 statement timeout |
| `MCP_API_KEY` | (空) | 舊部署相容性 bootstrap；僅在 SQLite 沒有任何 Key 時匯入一次 |
| `MCP_API_KEY_DB_PATH` | `data/mcp-api-keys.db` | API Key 管理 SQLite 路徑；容器預設 `/data/mcp-api-keys.db` |
| `MCP_API_KEY_PEPPER` | (必填) | 至少 32 bytes 的 HMAC-SHA-256 pepper；只放 secret manager，不存 SQLite |
| `MCP_ADMIN_TOKEN` | (必填) | 至少 32 bytes 的獨立管理 Bearer token，不可與 MCP Key 共用 |
| `MCP_TRUSTED_ORIGINS` | (空) | 額外信任的跨來源 Origin,逗號分隔,例如 `https://a.example,https://b.example`。非瀏覽器用戶端不受影響,詳見「跨來源保護」 |
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
curl http://127.0.0.1:3000/healthz   # liveness:進程是否活著
# {"status":"ok"}

curl http://127.0.0.1:3000/readyz    # readiness:資料來源是否可用
# {"status":"ok"}        資料來源正常
# {"status":"unavailable","reason":"資料來源目前不可用"}   → HTTP 503
```

## MCP endpoint 與驗證

- `POST /mcp`:MCP JSON-RPC 請求
- `GET /mcp`:Streamable HTTP 的 SSE 串流/可恢復連線(由官方 SDK 處理)
- `DELETE /mcp`:終止 MCP session

所有 MCP 請求仍沿用原本的 header，不需修改 Client:

```http
Authorization: Bearer <MCP_API_KEY>
```

驗證失敗回傳 HTTP 401 且不會查詢資料庫;超過 rate limit 回傳 HTTP 429。

MCP client 設定範例(以 Claude Code 為例):

```bash
claude mcp add --transport http stock-mcp https://your-domain.example/mcp \
  --header "Authorization: Bearer <MCP_API_KEY>"
```

## 多組 MCP API Key 管理

管理頁位於：

```text
https://your-domain.example/admin/mcp-api-keys
```

頁面會要求輸入獨立的 `MCP_ADMIN_TOKEN`。此 token 只存在目前頁面的記憶體，不寫入 Cookie、URL、`localStorage` 或 `sessionStorage`；管理 API 使用同一個 `Authorization: Bearer <MCP_ADMIN_TOKEN>` header。管理頁 HTML 本身不含資料或秘密，所有資料 API 都需要管理者驗證並套用 Origin 保護。

可用功能包含 List、Create、Edit、Enable、Disable、Rotate、Delete、Refresh，以及建立／輪替後的一次性 Copy。完整 API Key 只在 Create 或 Rotate 成功回應中出現一次，關閉視窗後無法透過 List、Get 或 Update 取回。

新 Key 格式：

```text
mcp_live_<public-id>_<256-bit-url-safe-secret>
```

SQLite 只保存 public prefix 與 `HMAC-SHA-256(pepper, full-key)`，不保存明文或可逆密文。驗證先由 public ID 直接定位 snapshot 中的單筆 credential，再以常數時間比較 HMAC；MCP request 熱路徑不查 SQLite。舊格式 `MCP_API_KEY` 匯入後也用 HMAC 派生的 lookup prefix 直接定位，不需掃描全表。

### 管理 REST API

所有端點都要求 `Authorization: Bearer <MCP_ADMIN_TOKEN>`、限制 request body、回傳 `Cache-Control: no-store`，錯誤格式固定為 `{"error":{"code":"...","message":"..."}}`。

| Method | Endpoint | 功能 |
|---|---|---|
| `GET` | `/api/admin/mcp-api-keys` | 清單 |
| `POST` | `/api/admin/mcp-api-keys` | 建立；輸入 `name`、`description`、`expiresAt` |
| `GET` | `/api/admin/mcp-api-keys/{id}` | 非敏感 metadata |
| `PATCH` | `/api/admin/mcp-api-keys/{id}` | 更新 name／description／expiresAt；需帶 version |
| `POST` | `/api/admin/mcp-api-keys/{id}/enable` | 啟用；body 為 `{"version":N}` |
| `POST` | `/api/admin/mcp-api-keys/{id}/disable` | 停用；body 為 `{"version":N}` |
| `POST` | `/api/admin/mcp-api-keys/{id}/rotate` | 輪替並一次性回傳新 Key |
| `DELETE` | `/api/admin/mcp-api-keys/{id}` | soft-delete／revoke；body 為 `{"version":N}` |

Create 範例：

```json
{
  "name": "Production Client",
  "description": "Used by production MCP client",
  "expiresAt": null
}
```

Create／Rotate 成功回應才會額外包含：

```json
{
  "item": {
    "id": "...",
    "name": "Production Client",
    "maskedKey": "mcp_live_ab12..._••••••••••••",
    "status": "active",
    "version": 1
  },
  "apiKey": "mcp_live_ab12..._<one-time-secret>",
  "notice": "完整 API Key 只顯示這一次，關閉後無法再次取得。"
}
```

`version` 是樂觀鎖；過期或重複操作會回 409。停用或刪除最後一組尚未到期的 active Key 也會回 409，避免 MCP 存取被意外全部鎖死；即使如此，管理存取仍由獨立 `MCP_ADMIN_TOKEN` 保護，可用來建立替代 Key。

### 即時生效與 last-used

管理 transaction 會在同一筆 SQLite transaction 內取得完整 active credentials，commit 成功後以 `atomic.Pointer` 一次替換不可變 snapshot，然後才回應管理 API。停用、刪除或輪替後，後續新請求立即讀到新 snapshot；已進入 handler 的請求不強制中止。snapshot 建立是純記憶體操作，不存在 DB commit 成功後 reload I/O 失敗而保留 revoked Key 的窗口；明確 Reload 若失敗則保留上一份完整 snapshot，不發布 partial state。

成功驗證後的 `last_used_at` 以記憶體節流，同一 Key 最多每分鐘排程一次，背景批次更新 SQLite；寫入失敗不影響 MCP request，graceful shutdown 會盡力 flush。

### 舊 `MCP_API_KEY` 遷移

啟動時若 SQLite 沒有任何 Key 且 `MCP_API_KEY` 有值，會匯入為 `Migrated MCP_API_KEY`，不在 log 輸出原值。資料庫已有任何 Key 時不再重複匯入。確認管理頁可以使用後，可從部署環境移除 `MCP_API_KEY`；它不是 CRUD 的持久化來源。

### SQLite、備份與 Pepper

- migration 是可重複執行的 `CREATE TABLE/INDEX IF NOT EXISTS`，啟用 foreign keys、5 秒 busy timeout 與 WAL。
- 程式會以 `0700` 建立資料夾並盡力將 DB 設為 `0600`。資料檔、`-wal`、`-shm` 已列入 `.gitignore`。
- Docker Compose 使用 named volume `mcp-api-key-data:/data`。自行 `docker run` 時也必須掛載 `/data`，否則重建容器會失去 Key。
- 執行中備份請使用 SQLite backup 工具或先正常停止服務，再一起複製 `.db`、`-wal`、`-shm`；還原前先停止服務。備份雖不含明文 Key，仍應視為敏感資料。
- Pepper 必須另存於 secret manager／離線備份，不在 SQLite 內。資料庫保存 pepper check；設定錯誤或遺失時服務會明確拒絕啟動，不會靜默使用空 pepper。恢復方式是還原正確 pepper；若永久遺失，只能保留舊 DB 作稽核備份、改用全新 DB＋新 pepper，並由 `MCP_API_KEY` bootstrap 或管理頁重新建立所有 Client Key。
- Pepper 不可直接原地輪替，因為既有 HMAC 無法在沒有明文 Key 的情況下重算。要換 pepper，需建立新 DB 並輪替所有 Client。

### 單一與多執行個體

此實作明確支援「單一 Go process＋本機 SQLite volume」。SQLite 檔案不應由多台主機或多個 replica 共用，記憶體 snapshot 也不會跨程序同步；因此不可將同一份 `/data` 掛到多個 replicas 後宣稱一致性。需要水平擴充時，應先把 Key repository 移到部署環境已有的共享關聯式資料庫，再加入 version polling／通知機制；本功能沒有導入 Redis 或 message broker。

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

### `get_monthly_revenue_history`

僅在 `DATA_SOURCE=api` 出現。查詢單一股票的月營收歷史（當月/累計營收、月增率、年增率、當月股價高低均價），依月份新到舊排序。`from`/`to` 為 `YYYY-MM` 月份區間（選填），`limit` 預設 24（1–120）。查無資料回傳空陣列；股票不存在回傳 tool error「找不到股票代號:{symbol}」。

```json
{"name": "get_monthly_revenue_history", "arguments": {"symbol": "2330", "from": "2024-01", "to": "2026-06", "limit": 24}}
```

### `get_financial_statement_history`

僅在 `DATA_SOURCE=api` 出現。查詢單一股票的季/年度財報（毛利率、營益率、稅前/稅後淨利率、每股淨值、每股營收、EPS、ROE、ROA）。`period_type` 可選 `quarterly`（預設）、`annual`、`all`；`quarter` 欄位以 `A` 代表年度資料、`Q1`–`Q4` 代表季度。`limit` 預設 12（1–40）。

```json
{"name": "get_financial_statement_history", "arguments": {"symbol": "2330", "period_type": "quarterly", "limit": 12}}
```

### `get_dividend_history`

僅在 `DATA_SOURCE=api` 出現。查詢單一股票的歷年股利（現金/股票股利與其盈餘/公積細項、盈餘分配率、除息日/除權日/發放日）。`from_year`/`to_year` 依**股利所屬年度**（`dividend_year`）篩選，範圍 1990 至目前年度加一；`limit` 預設 20（1–80）。尚未公布的日期一律為 `null`。

```json
{"name": "get_dividend_history", "arguments": {"symbol": "2330", "from_year": 2020, "to_year": 2026, "limit": 30}}
```

> 三個歷史財務工具的免責聲明與價格工具不同：`本資料來自 stock_rust 已蒐集與計算的歷史資料,可能有延遲,僅供資訊參考,不構成投資建議。`

### `get_stock_valuation`

僅在 `DATA_SOURCE=api` 出現。查詢個股最新或指定日期以前最近一筆估值模型結果；指定日期採 31 天回溯窗口，股票存在但窗口內沒有資料時 `valuation` 與 `data_as_of` 都是 `null`。`cheap`、`fair`、`expensive` 是歷史模型算出的估值分界，`valuation_band` 為 `undervalued`、`fair_valued`、`overvalued` 或 `highly_overvalued`，不是目標價或買賣建議。

```json
{"name": "get_stock_valuation", "arguments": {"symbol": "2330", "date": "2026-07-16"}}
```

### `get_market_breadth`

僅在 `DATA_SOURCE=api` 出現。查詢市場漲跌家數、5/20/60/120/240 日均線上下家數及估值分布。`market` 可選 `all`（預設）、`twse`、`tpex`；`days` 預設 1、範圍 1–60，回傳最近有統計資料的交易日，不補非交易日。`history` 固定由新到舊，`breadth` 固定等於 `history[0]`。

```json
{"name": "get_market_breadth", "arguments": {"market": "all", "date": "2026-07-16", "days": 20}}
```

### `get_dividend_yield_ranking`

僅在 `DATA_SOURCE=api` 出現。查詢指定日期以前最近資料日的殖利率排行，可依市場與正整數 `industry_id` 篩選，`limit` 預設 20（1–50）。`market=all` 只包含上市與上櫃，不包含公開發行及興櫃；未知產業或合法條件查無資料時 `stocks` 為空陣列。

```json
{"name": "get_dividend_yield_ranking", "arguments": {"date": "2026-07-16", "market": "twse", "industry_id": 24, "limit": 20}}
```

> 三個分析工具都回傳繁體中文摘要與完整 `structuredContent`，並包含 `data_kind`、Data API 決定的 `data_as_of`、`is_realtime: false` 與分析型免責聲明；工具不提供買進、賣出、目標價或報酬保證。

### `screen_stocks`

僅在 `DATA_SOURCE=api` 出現。用固定白名單條件篩選股票：單一市場（`twse`/`tpex`）、產業、估值區間、最低營收年增率、EPS、ROE 或殖利率。至少要有一個實質條件；預設範圍 `market=all`、排序與 `limit` 不算篩選條件，避免把工具當成全市場資料匯出。

`sort_by` 只允許 `stock_symbol`、`revenue_yoy`、`eps`、`roe`、`dividend_yield`、`valuation_percentage`，`sort_order` 只允許 `asc`/`desc`，`limit` 預設 20（1–50）。每檔股票採自己最新且仍在新鮮度期限內的資料，因此結果分別附 `revenue_month`、`financial_period`、`valuation_date`、`yield_date`；過期或缺失指標維持 `null`，不可假設所有指標來自同一天。

```json
{"name": "screen_stocks", "arguments": {"market": "twse", "industry_id": 24, "valuation_band": "undervalued", "min_revenue_yoy_percent": 10, "min_eps": 5, "min_roe_percent": 10, "min_dividend_yield_percent": 3, "sort_by": "dividend_yield", "sort_order": "desc", "limit": 20}}
```

回應的 `data_kind` 為 `stock_screening_result`，混合指標沒有單一正確資料日，所以最外層 `data_as_of` 固定為 `null`；查無符合項目時 `stocks` 為空陣列。結果只描述歷史資料符合情形，不替使用者做投資決策。

### `get_market_index_history`

僅在 `DATA_SOURCE=api` 出現。查詢台股大盤 TAIEX 加權指數的歷史走勢（收盤指數、漲跌點數、成交金額/筆數/股數），依日期新到舊排序，與 `get_market_breadth` 互補（前者看指數點位，後者看市場內部強弱）。`from`/`to` 為 `YYYY-MM-DD` 日期區間（選填，`from` 不可晚於 `to`），`limit` 預設 30（1–365）。查無資料回傳空陣列且 `data_as_of` 為 `null`（大盤指數沒有「代號不存在」的 404 語意）。

```json
{"name": "get_market_index_history", "arguments": {"from": "2026-06-01", "to": "2026-07-17", "limit": 30}}
```

回應的 `data_kind` 為 `market_index_history`，`data_as_of` 取實際回傳資料最新一筆的日期；`index`、`change`、`trade_value`、`transaction`、`trading_volume` 缺值一律為 `null`。

### `get_dividend_calendar`

僅在 `DATA_SOURCE=api` 出現。查詢日期區間內的除權息與股利發放行事曆，依事件日期由近到遠（`event_date ASC`）排序。`event_type` 可選 `ex_dividend`（除息）、`ex_rights`（除權）、`cash_payable`（現金股利發放）、`stock_payable`（股票股利發放）或 `all`（預設）；`from` 未提供時預設查詢當日、`to` 預設 `from` 加 30 天，兩者都提供時 `from` 不可晚於 `to` 且區間不可超過 92 天；`limit` 預設 50（1–200）。同一筆股利若有多個日期落在區間內會輸出多筆事件（每筆一個 `event_type`）。

```json
{"name": "get_dividend_calendar", "arguments": {"from": "2026-07-01", "to": "2026-07-31", "event_type": "all", "limit": 50}}
```

回應的 `data_kind` 為 `dividend_calendar`；混合事件沒有單一統計日期，最外層 `data_as_of` 固定為 `null`，各事件日期在每筆 `event_date` 內。查無事件時 `events` 為空陣列。

### `get_qfii_holding_ranking`

僅在 `DATA_SOURCE=api` 出現。查詢外資（QFII）持股比例或持股數排行，可依市場與正整數 `industry_id` 篩選；`market=all` 只包含上市與上櫃。`sort_by` 可選 `percentage`（外資持股比例，預設）或 `shares`（外資持股數），一律由高到低；`limit` 預設 20（1–50）。已排除暫停上市與外資零持股的股票。

> **快照限制**：這是**最近一次每日更新的當前快照**，資料庫沒有歷史序列，因此**無法回答「外資最近增持/減持了哪些股票」這類趨勢問題**；`data_as_of` 也因為沒有列級更新日期而固定為 `null`，摘要文字會明確標示快照語意。

```json
{"name": "get_qfii_holding_ranking", "arguments": {"market": "twse", "industry_id": 24, "sort_by": "percentage", "limit": 20}}
```

回應的 `data_kind` 為 `qfii_holding_ranking`；`qfii_shares_held` 與 `issued_share` 為整數股數，缺值為 `null`。查無資料時 `stocks` 為空陣列。

## 安全設計

- 股票資料來源維持唯讀；PostgreSQL SQL 集中在 `stock/repository.go` 且全部參數化。API Key 只寫入獨立 SQLite。
- 多組 API key 以 HMAC-SHA-256＋server-side pepper 保存驗證資料，並以 `crypto/subtle` 常數時間比較。
- rate limit 以「非敏感 Key ID + 來源 IP」計數;預設 60 次/分鐘,可由環境變數調整。
- 管理 API 使用獨立 admin token、Origin 防護、64 KiB body limit 與 `no-store`。
- 停用、輪替、soft-delete 後以 atomic snapshot 立即失效；最後一組 active Key 不可停用或刪除。
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
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_buffering off;   # SSE 串流必須關閉緩衝
    proxy_read_timeout 3600s;
}

location = /healthz {
    proxy_pass http://127.0.0.1:3000/healthz;
}

location = /readyz {
    proxy_pass http://127.0.0.1:3000/readyz;
}

# 建議再以 VPN、IP allowlist 或額外邊界驗證限制管理路由。
location /admin/ {
    proxy_pass http://127.0.0.1:3000;
    proxy_set_header Host $host;
}

location /api/admin/ {
    proxy_pass http://127.0.0.1:3000;
    proxy_set_header Host $host;
}
```

放在代理後方時將 `TRUST_PROXY=true`,rate limit 才會使用真實用戶端 IP。

**`TRUSTED_PROXY_HOPS` 必須與實際的代理層數一致。** `X-Forwarded-For` 是逗號分隔的清單,每經過一層代理就在**最右邊**附加一個 IP;清單左邊的部分是用戶端自己送來的,任何人都能偽造。以上面的 Nginx 設定為例,本服務收到的值是:

```
X-Forwarded-For: <用戶端自己填的內容>, <連到 Nginx 的真實來源 IP>
```

服務因此從右邊往左數,跳過 `TRUSTED_PROXY_HOPS - 1` 項後取用。設定值對照:

| 部署拓撲 | `TRUSTED_PROXY_HOPS` |
| --- | --- |
| 用戶端 → Nginx → 本服務 | `1`(預設) |
| 用戶端 → Cloudflare/CDN → Nginx → 本服務 | `2` |

設得**太大**會取到清單更左邊、由用戶端可控的位置,讓攻擊者每個請求偽造一個假 IP 就能取得無限的限流額度;設得**太小**則會把代理自己的位址當成用戶端,使所有人共用同一份額度。層數與清單長度不符、或取出的值不是合法 IP 時,服務會退回使用 TCP 連線的 `RemoteAddr`(寧可過度限流,也不採信可能被偽造的值)。

### 健康檢查

| 端點 | 用途 | 行為 |
| --- | --- | --- |
| `GET /healthz` | liveness(存活) | 只要 HTTP 伺服器能回應就回 200,不碰任何外部相依 |
| `GET /readyz` | readiness(就緒) | 額外確認資料來源(Data API 或資料庫)可用;不可用時回 503 |

兩者都不需要 API key。負載平衡器與 k8s 應使用 `/readyz` 決定是否把流量導向這個實例,`/healthz` 則用於判斷是否需要重啟容器。`/readyz` 的檢查結果會快取 2 秒,避免高頻探測本身變成後端的額外負載;回應內容不含任何底層錯誤細節(這個端點不需認證,而底層錯誤可能包含內網位址)。

兩個端點的請求 log 為 `debug` 等級,不會在預設的 `info` 等級產生洗版。

執行檔本身也提供健康檢查模式,供容器 `HEALTHCHECK` 使用:

```bash
stock-mcp -health-check   # 呼叫本機 /readyz,結束碼 0 = 就緒,1 = 未就緒
```

之所以需要這個模式,是因為執行階段 image 用的是 `gcr.io/distroless/static`——裡面沒有 shell、curl 或 wget,無法用常見的 `HEALTHCHECK CMD curl -f ...` 寫法。讓執行檔自己提供檢查模式是 distroless 的標準解法,不需要為了健康檢查而在 image 裡多裝任何工具。`Dockerfile` 與 `docker-compose.example.yml` 都已接上。

### 跨來源保護

服務會擋下 `Origin` 與自身 `Host` 不符的請求(回 403),符合 MCP 安全最佳實務對 Origin 驗證的要求。

- **非瀏覽器用戶端不受影響**:Claude Desktop、Claude Code、MCP Inspector、`curl` 等都不會送出 `Origin` header,沒有 `Origin` 的請求一律放行。
- 若反向代理會改寫 `Host`(例如對外是 `mcp.example.com`、轉發時改成 `127.0.0.1:3000`),請在 Nginx 加上 `proxy_set_header Host $host;`,或把對外網域加入 `MCP_TRUSTED_ORIGINS`。
- 需要讓特定網頁前端跨網域呼叫時,把該來源加入 `MCP_TRUSTED_ORIGINS`。

## MCP 協定相容性

| 項目 | 說明 |
| --- | --- |
| Transport | 僅 Streamable HTTP(單一 endpoint 處理 POST / GET / DELETE)。**不支援 stdio** |
| 支援的 Protocol Version | `2025-11-25`(預設)、`2025-06-18`、`2025-03-26`、`2024-11-05` |
| 版本協商 | 由 go-sdk 依用戶端送出的 `protocolVersion` 自動協商;送出不支援的版本時回退到最新版 |
| Capabilities | 僅 Tools(無 Resources / Prompts / Sampling);全部工具皆標記 `readOnlyHint: true` |
| Session | 由 SDK 管理 `Mcp-Session-Id`;**閒置逾時 5 分鐘**,逾時後用戶端下一次請求會收到 404 並自動重新 `initialize`(規範定義的正常流程) |
| SSE resume | 未啟用 `EventStore`,不支援 `Last-Event-ID` 續傳。所有工具皆為唯讀查詢,重連後重查即可 |
| 請求大小上限 | 單一請求 body 上限 1 MiB,超過回 413 |

> 支援的版本清單取決於 `go.mod` 裡的 `github.com/modelcontextprotocol/go-sdk` 版本(目前 v1.6.1);升級 SDK 時請一併確認此表。

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
- rate limiter 為單機記憶體實作；水平擴充時每個 instance 會各自計數。
- API Key repository 與驗證 snapshot 是單一程序＋本機 SQLite 設計，不支援多 replica 共用同一 SQLite volume。
- 第一版資料來源僅限既有 PostgreSQL,無任何即時行情來源。
