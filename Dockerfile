# ── 建置階段 ────────────────────────────────────────────────────────────
FROM golang:1.27rc2 AS build

WORKDIR /src

# 先複製模組定義,讓相依套件下載能被 Docker layer 快取
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/stock-mcp .

# ── 執行階段:distroless 非 root 使用者,不含 shell 與套件管理器 ──────────
FROM gcr.io/distroless/static-debian13:nonroot

COPY --from=build /out/stock-mcp /stock-mcp

# 容器內需綁定 0.0.0.0 才能對外服務(以環境變數覆蓋預設的 127.0.0.1)
ENV HOST=0.0.0.0
EXPOSE 3000

USER nonroot

# distroless image 裡沒有 shell、curl 或 wget,無法用常見的
# `CMD curl -f http://localhost:3000/readyz` 寫法做健康檢查。改由執行檔
# 自己提供 -health-check 模式(見 main.go 的 runHealthCheck):它會呼叫
# 本機的 /readyz 並以結束碼回報結果,不需要在 image 裡多裝任何工具,
# 也就不會破壞 distroless 的最小攻擊面。
#
# 必須用 exec 形式(JSON 陣列),因為 shell 形式需要 /bin/sh。
# start-period 給服務啟動與首次連上資料來源的緩衝時間,這段期間檢查
# 失敗不會計入 retries。
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD ["/stock-mcp", "-health-check"]

ENTRYPOINT ["/stock-mcp"]
