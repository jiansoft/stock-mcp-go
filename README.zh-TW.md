# stock-mcp-go

[English](README.md) | [繁體中文](README.zh-TW.md)

以 Go 與官方 MCP Go SDK 打造的台股唯讀 Model Context Protocol（MCP）伺服器，透過 stateless Streamable HTTP 提供股票報價、公司基本資料、財務歷史、估值、選股與市場分析工具。

> [!IMPORTANT]
> 本服務大多數資料都是歷史資料或上游資料庫目前最新可取得的資料，不是交易所逐筆行情，也不保證即時。唯一可能回傳即時結果的工具是盤中使用的 `get_market_movers`。所有回應都會附上資料來源、新鮮度與免責聲明欄位。

## 功能特色

- Stateless MCP Streamable HTTP endpoint，所有工具皆標示為唯讀
- 由 `stock_rust` Data API 提供 16 個唯讀工具
- 回傳繁體中文摘要與可供程式處理的 structured content
- 支援多組 MCP API Key 的建立、編輯、啟用、停用、輪替與撤銷
- 使用 HMAC-SHA-256 與 server-side pepper 驗證 API Key，不保存明文 Key
- 內嵌 API Key 管理介面，使用安全的 8 小時 HttpOnly session
- 依 API Key 與用戶端 IP 進行流量限制
- Origin 驗證、request body 上限、結構化 log 與 graceful shutdown
- 提供 liveness 與會實際檢查資料來源的 readiness endpoint
- Distroless、non-root 容器映像與內建健康檢查

## 系統架構

```text
MCP client
    │  HTTPS + Authorization: Bearer <MCP_API_KEY>
    ▼
反向代理
    ▼
stock-mcp-go
    ├── /mcp                         stateless MCP endpoint
    ├── /healthz 與 /readyz          健康檢查
    ├── /admin/mcp-api-keys          內嵌管理介面
    ├── web/                          驗證、限流與 HTTP 安全
    ├── apikey/                       SQLite Key store 與記憶體 snapshot
    └── stock/                        MCP tools 與 Data API client
          └── api 模式 ─────────────► stock_rust Data API
```

> [!WARNING]
> 直連 PostgreSQL 的 `db` 模式已棄用，只暫時保留作為遷移期比對，並將在後續版本移除。新部署請勿使用 DB 模式。目前 DB 模式只提供 `search_stock`、`get_latest_daily_quote`、`get_price_history` 與 `get_stock_profile`。

## MCP 工具

| 工具 | 適用模式 | 功能 |
| --- | --- | --- |
| `search_stock` | API | 依股票代號或中英文名稱搜尋 |
| `get_latest_daily_quote` | API | 查詢最新可取得的日報價 |
| `get_price_history` | API | 依日期區間查詢歷史日線 |
| `get_stock_profile` | API | 查詢基本資料、報價、EPS、ROE、每股淨值與歷史高低點 |
| `get_realtime_snapshot` | API | 查詢第三方近即時快照；不保證即時 |
| `get_monthly_revenue_history` | API | 查詢單月與累計營收歷史 |
| `get_financial_statement_history` | API | 查詢季度或年度財務指標 |
| `get_dividend_history` | API | 查詢歷年現金與股票股利 |
| `get_stock_valuation` | API | 查詢最新或指定日期以前的估值模型結果 |
| `get_market_breadth` | API | 查詢漲跌家數、均線上下家數與估值分布 |
| `get_dividend_yield_ranking` | API | 查詢上市櫃股票歷史殖利率排行 |
| `screen_stocks` | API | 以白名單內的基本面與估值條件篩選股票 |
| `get_market_index_history` | API | 查詢台股加權指數歷史 |
| `get_dividend_calendar` | API | 查詢除權息與股利發放事件 |
| `get_qfii_holding_ranking` | API | 查詢最新外資持股快照排行 |
| `get_market_movers` | API | 查詢漲幅、跌幅或成交量排行，自動選擇盤中或收盤資料 |

所有工具都是唯讀。輸出包含 `data_kind`、`data_as_of`、`is_realtime` 與免責聲明；缺失資料維持 `null`，不會以 0 或推測值代替。

## 系統需求

- Go 1.27（目前 `go.mod` 使用 `go1.27rc2`）
- 預設 `api` 模式：可連線的相容 `stock_rust` Data API 與其 Bearer Key
- 正式環境：Caddy、Nginx 等 HTTPS 反向代理

## 快速開始

```bash
git clone https://github.com/<owner>/stock-mcp-go.git
cd stock-mcp-go
cp .env.example .env
```

預設 API 模式至少需要設定：

```dotenv
DATA_SOURCE=api
STOCK_RUST_API_BASE_URL=http://127.0.0.1:9002
STOCK_RUST_API_KEY=replace-with-a-dedicated-upstream-key
MCP_API_KEY=replace-with-an-initial-client-key
MCP_API_KEY_PEPPER=replace-with-at-least-32-random-bytes
MCP_ADMIN_TOKEN=replace-with-a-different-32-byte-random-secret
```

`MCP_API_KEY` 只用於 bootstrap。API Key 的 SQLite 資料庫為空時會匯入一次；確認匯入成功後即可從環境變數移除。

啟動服務：

```bash
go run .
```

專案內的 `.env.example` 使用 port `9005`；若未設定 `PORT`，程式預設值則是 `3000`。

確認服務狀態：

```bash
curl http://127.0.0.1:9005/healthz
curl http://127.0.0.1:9005/readyz
```

正常回應為 `{"status":"ok"}`。選定的資料來源無法使用時，`/readyz` 會回 HTTP 503。

## MCP Client 設定

預設 endpoint 是 `/mcp`，接受帶有驗證資訊的 `POST` 請求：

```http
Authorization: Bearer <MCP_API_KEY>
```

Claude Code 範例：

```bash
claude mcp add --transport http stock-mcp https://mcp.example.com/mcp \
  --header "Authorization: Bearer <MCP_API_KEY>"
```

本服務使用 stateless Streamable HTTP，不提供 stdio transport、伺服器端 session 或 SSE 訂閱 endpoint；`GET /mcp` 與 `DELETE /mcp` 會回 `405 Method Not Allowed`。

## 環境變數

| 變數 | 預設值 | 說明 |
| --- | --- | --- |
| `APP_ENV` | `development` | `development`、`production` 或 `test` |
| `HOST` | `127.0.0.1` | 監聽位址；容器內使用 `0.0.0.0` |
| `PORT` | `3000` | HTTP port（`.env.example` 會覆寫為 `9005`） |
| `MCP_PATH` | `/mcp` | MCP endpoint 路徑 |
| `TRUST_PROXY` | `false` | 是否信任代理附加的用戶端 IP 資訊 |
| `TRUSTED_PROXY_HOPS` | `1` | 會附加 `X-Forwarded-For` 的受信任代理層數 |
| `DATA_SOURCE` | `api` | 資料來源；新部署必須使用 `api` |
| `STOCK_RUST_API_BASE_URL` | API 模式必填 | 上游 Data API base URL |
| `STOCK_RUST_API_KEY` | API 模式必填 | 上游專用 Bearer Key |
| `API_TIMEOUT_MS` | `5000` | 上游 HTTP timeout |
| `MCP_API_KEY` | 空 | 一次性相容 bootstrap Key |
| `MCP_API_KEY_DB_PATH` | `data/mcp-api-keys.db` | API Key SQLite 路徑 |
| `MCP_API_KEY_PEPPER` | 必填 | 至少 32 bytes 的 HMAC pepper |
| `MCP_ADMIN_TOKEN` | 必填 | 至少 32 bytes 的獨立管理密鑰 |
| `MCP_TRUSTED_ORIGINS` | 空 | 逗號分隔的允許瀏覽器 Origin |
| `RATE_LIMIT_WINDOW_MS` | `60000` | Rate limit 視窗 |
| `RATE_LIMIT_MAX_REQUESTS` | `60` | 每組 API Key 與來源 IP 在視窗內的請求上限 |
| `LOG_LEVEL` | `info` | `debug`、`info`、`warn` 或 `error` |

上游 Key、MCP Client Key、pepper 與 admin token 都必須使用不同的秘密。請勿提交 `.env`。

已棄用的 DB 相容模式另外使用 `DATABASE_URL`、`DB_POOL_MAX`、`DB_CONNECTION_TIMEOUT_MS` 與 `DB_STATEMENT_TIMEOUT_MS`；DB 模式移除後，這些設定也會一併消失。

## API Key 管理

開啟內嵌管理介面：

```text
https://mcp.example.com/admin/mcp-api-keys
```

第一次使用 `MCP_ADMIN_TOKEN` 登入後，伺服器會換發只存在記憶體、有效 8 小時的 `HttpOnly`、`Secure`、`SameSite=Strict` session cookie。Token 不會寫入瀏覽器儲存空間。

自動化程式也可以用 `Authorization: Bearer <MCP_ADMIN_TOKEN>` 呼叫管理 API：

| Method | Endpoint | 功能 |
| --- | --- | --- |
| `POST` / `DELETE` | `/api/admin/session` | 登入或登出 |
| `GET` / `POST` | `/api/admin/mcp-api-keys` | 列出或建立 Key |
| `GET` / `PATCH` / `DELETE` | `/api/admin/mcp-api-keys/{id}` | 讀取、更新或撤銷 Key |
| `POST` | `/api/admin/mcp-api-keys/{id}/enable` | 啟用 Key |
| `POST` | `/api/admin/mcp-api-keys/{id}/disable` | 停用 Key |
| `POST` | `/api/admin/mcp-api-keys/{id}/rotate` | 輪替 Key |

完整 API Key 只會在建立或輪替後回傳一次。SQLite 只保存 public prefix 與 `HMAC-SHA-256(pepper, full-key)`，不保存明文。資料庫 transaction commit 後，變更會立刻發布到不可變的記憶體 snapshot。

API Key store 的設計範圍是單一 Go process 搭配一個本機 SQLite volume。不可讓多個 replica 共用同一個 SQLite 檔案；多 replica 部署需要共享的關聯式 Key store 與跨程序 refresh 機制。

## Docker

從原始碼建置並啟動：

```bash
docker compose -f docker-compose.example.yml up --build
```

Compose 範例會把 `127.0.0.1:9004` 映射到容器 port `3000`，並用 `mcp-api-key-data` named volume 保存 API Key 狀態。映像以 non-root 身分執行；因 distroless runtime 沒有 shell、`curl` 或 `wget`，健康檢查會使用執行檔內建的 `-health-check` 模式。

`Dockerfile_live` 與 `control.sh` 則提供另一套預先編譯 ARM binary 的部署流程。`build.ps1` 支援 Linux ARM64 與 ARMv7，部署時需把對應的 `stock-mcp_linux_*` binary 放在部署目錄根層。

## 反向代理

正式環境必須使用 HTTPS。最小 Caddy 設定：

```caddyfile
mcp.example.com {
    reverse_proxy 127.0.0.1:9005
}
```

最小 Nginx 設定：

```nginx
location / {
    proxy_pass http://127.0.0.1:9005;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
}
```

只有服務確實位於受信任的反向代理後方時，才設定 `TRUST_PROXY=true`。`TRUSTED_PROXY_HOPS` 必須等於會附加 `X-Forwarded-For` 的代理層數：Client → Nginx → Server 使用 `1`；Client → CDN → Nginx → Server 使用 `2`。設得太大可能讓 rate limit 使用的 IP 身分可被偽造。

可以的話，請在網路邊界額外限制管理路由：

- `/admin/`
- `/api/admin/`

## 資料與安全語意

- `get_market_movers` 是唯一 `is_realtime` 可能為 `true` 的工具。
- 台股收盤到最終日線資料產生前，movers 可能回退到前一交易日；回應會標明實際來源與日期。
- `get_realtime_snapshot` 是第三方近即時快照，但刻意標示為不保證即時。
- 估值區間、排行與選股結果只描述歷史模型輸出，不是目標價或交易建議。
- 外資持股是最新快照而非時間序列，無法用來推論增持或減持趨勢。
- MCP request body 上限為 1 MiB；管理 API request body 上限為 64 KiB。
- 驗證失敗請求有獨立限流，通過驗證後才套用一般的每 Key 限流。
- Log 不會記錄秘密或 Authorization header。

## 開發指令

```bash
make test        # go test ./...
make lint        # go vet ./...
make fmt-check   # 檢查 gofmt
make build       # 建置 ./stock-mcp
```

PostgreSQL 整合測試需要明確啟用：

```bash
TEST_DATABASE_URL=postgresql://... go test ./stock/ -run TestRepositoryIntegration -v
```

## 已知限制

- 資料新鮮度取決於上游 `stock_rust` 的蒐集與處理排程。
- 直連 PostgreSQL 模式已棄用，只提供 4 個核心工具，並將於後續版本移除。
- Rate limit 計數與 API Key 驗證 snapshot 都只存在單一 process。
- SQLite API Key repository 只支援單一 process 與本機持久化 volume。
- 本服務只提供 MCP Tools，不提供 Resources、Prompts 或 Sampling。
- 所有輸出僅供資訊參考，不構成投資建議。
