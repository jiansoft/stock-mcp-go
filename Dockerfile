# ── 建置階段 ────────────────────────────────────────────────────────────
FROM golang:1.27rc1 AS build

WORKDIR /src

# 先複製模組定義,讓相依套件下載能被 Docker layer 快取
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/stock-mcp .

# ── 執行階段:distroless 非 root 使用者,不含 shell 與套件管理器 ──────────
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/stock-mcp /stock-mcp

# 容器內需綁定 0.0.0.0 才能對外服務(以環境變數覆蓋預設的 127.0.0.1)
ENV HOST=0.0.0.0
EXPOSE 3000

USER nonroot
ENTRYPOINT ["/stock-mcp"]
