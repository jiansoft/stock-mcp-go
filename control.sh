#! /bin/bash

# ==============================================================================
# 配置變量
# ==============================================================================

# 依 `uname -m` 判斷目前硬體架構，並選用 build.ps1 產出的對應執行檔。
detect_binary_name() {
  local machine_arch
  machine_arch="$(uname -m)"

  case "$machine_arch" in
    aarch64|arm64)
      echo "stock-mcp_linux_arm64"
      ;;
    armv7l|armv7)
      echo "stock-mcp_linux_armv7"
      ;;
    *)
      echo ">>> 錯誤：不支援的硬體架構：$machine_arch" >&2
      return 1
      ;;
  esac
}

if ! BINARY_NAME="$(detect_binary_name)"; then
  exit 1
fi
export BINARY_NAME

IMAGE_NAME="stock-mcp-image"
CONTAINER_NAME="stock-mcp-container"
LOG_BACKUP_DIR="./log_backup"
DEPLOY_DIR="/opt/stock_mcp"

# Docker 運行參數
#
# - 秘密不烘進 image(見 Dockerfile_live),改由 --env-file 在啟動時注入,
#   .env 需放在本腳本執行目錄(即 $DEPLOY_DIR)。
# - -e HOST / -e PORT 放在 --env-file 之後:docker 的 -e 優先權高於 --env-file,
#   可確保不論 .env 內怎麼設定,容器內一律綁定 0.0.0.0:3000,port mapping 不會失效。
# - 對外 port 為 9004(host 端,綁定所有網卡供區網直連),映射到容器內固定的 3000。
#   注意:Docker 發布的 port 會繞過 ufw/firewalld;流量為明文 HTTP,
#   MCP_API_KEY 以明文傳輸,僅適合信任的內網,跨網際網路務必改走反向代理 HTTPS。
# - log 走 stdout 由 json-file driver 接手,預設不輪替會無限成長,
#   限制單檔 10MB、最多 3 個,避免吃滿 SD 卡空間。
DOCKER_RUN_OPTS=(
  --name "$CONTAINER_NAME"
  --restart unless-stopped
  --log-opt max-size=10m
  --log-opt max-file=3
  --env-file .env
  -e TZ=Asia/Taipei
  -e HOST=0.0.0.0
  -e PORT=3000
  -e MCP_API_KEY_DB_PATH=/data/mcp-api-keys.db
  -v stock-mcp-api-key-data:/data
  -p 9005:3000
  -t
  -d
)

# ==============================================================================
# 核心功能
# ==============================================================================

function start() {
  local pid
  pid=$(pidof "$BINARY_NAME")
  if [ -z "$pid" ]; then
    # stock-mcp 以 slog 將 JSON log 寫到 stdout,這裡導向 nohup.out 保存。
    "./$BINARY_NAME" > nohup.out 2>&1 &
    echo ">>> $BINARY_NAME 已啟動"
  else
    echo ">>> $BINARY_NAME 已經在執行中 (PID: $pid)"
  fi
}

function stop() {
  if [ -f "./nohup.out" ]; then
    mkdir -p "$LOG_BACKUP_DIR"
    mv ./nohup.out "$LOG_BACKUP_DIR/nohup.out.$(date "+%Y%m%d-%H%M%S")"
  fi

  local pid
  pid=$(pidof "$BINARY_NAME")
  if [ -n "$pid" ]; then
    kill "$pid"
    echo ">>> $BINARY_NAME 已停止"
  else
    echo ">>> 找不到正在執行的 $BINARY_NAME 程序"
  fi
}

function restart() {
  stop
  sleep 1
  start
}

function move() {
  local src="/tmp/$BINARY_NAME"
  local dest="$DEPLOY_DIR/$BINARY_NAME"

  if [ -f "$src" ]; then
    local backup_name
    backup_name="$BINARY_NAME.$(date "+%Y%m%d-%H%M%S")"
    [ -f "./$BINARY_NAME" ] && mv "./$BINARY_NAME" "./$backup_name"

    mv "$src" "$dest"
    chmod +x "$dest"
    [ -f "./$backup_name" ] && chmod -x "./$backup_name"
    echo ">>> 檔案已成功從 $src 搬移至 $dest"
  else
    echo ">>> 檔案 $src 不存在，跳過搬移作業。"
  fi
}

function update() {
  stop
  sleep 1
  move
  sleep 1
  start
}

# ==============================================================================
# Docker 功能
# ==============================================================================

function docker_build() {
  echo ">>> 正在構建 Docker 映像檔：$IMAGE_NAME..."
  if docker build -t "$IMAGE_NAME" -f Dockerfile_live .; then
    docker system prune -f
    return 0
  else
    echo ">>> 錯誤：Docker 構建失敗"
    return 1
  fi
}

function docker_stop() {
  echo ">>> 正在停止並移除容器：$CONTAINER_NAME..."
  docker rm -f "$CONTAINER_NAME" 2>/dev/null || true
  docker ps
}

function docker_start() {
  echo ">>> 正在啟動容器：$CONTAINER_NAME..."
  docker run "${DOCKER_RUN_OPTS[@]}" "$IMAGE_NAME"
  docker ps
}

function docker_restart() {
  docker_stop
  sleep 1
  docker_start
}

function docker_update() {
  if docker_build; then
    docker_restart
  else
    echo ">>> 錯誤：因構建失敗，已中止 Docker 更新作業"
    exit 1
  fi
}

# ==============================================================================
# 入口與幫助
# ==============================================================================

function help() {
  echo "Usage: $0 {start|stop|restart|move|update|docker_build|docker_stop|docker_start|docker_restart|docker_update}"
}

case "$1" in
  start|stop|restart|move|update|docker_build|docker_stop|docker_start|docker_restart|docker_update)
    "$1"
    ;;
  *)
    help
    ;;
esac
