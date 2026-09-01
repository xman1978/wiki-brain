#!/usr/bin/env bash
# Wiki-Brain 服务启动/停止脚本
# 用法：./run.sh start | ./run.sh stop | ./run.sh restart | ./run.sh status

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

BIN_DIR="$ROOT_DIR/bin"
BIN_PATH="$BIN_DIR/wiki-brain-server"
BIN_NAME="$(basename "$BIN_PATH")"
CONFIG_PATH="$ROOT_DIR/config/config.yml"
PID_FILE="$ROOT_DIR/run.pid"
LOG_DIR="$ROOT_DIR/logs"
# 程序自身把 stdout/stderr 接管进 wiki-brain.log（见 internal/foundation/logging.go
# 的 RedirectStdToLogFile），这里只需要给 nohup 一个占位落点，正常情况下不会再有
# 内容写进来；万一程序在完成接管之前就崩溃（比如启动阶段极早期的 panic），这里
# 还能兜底看到内容。
STDOUT_LOG="$LOG_DIR/server.out.log"
PORT="$(sed -n 's/^[[:space:]]*port:[[:space:]]*\([0-9][0-9]*\).*/\1/p' "$CONFIG_PATH" | head -1)"
PORT="${PORT:-8800}"

build() {
    mkdir -p "$BIN_DIR"
    echo "构建中..."
    go build -o "$BIN_PATH" ./cmd/server
}

# 判活分两步：先看谁在监听配置里的端口，再核对那个进程的程序名是不是我们
# 自己的二进制（$BIN_NAME）——只查端口号不够，万一端口被别的无关程序占用
# （比如别人也用了 8800），会被误判成"服务已经在运行"而直接跳过启动。
port_pid() {
    lsof -ti "tcp:${PORT}" -sTCP:LISTEN 2>/dev/null | head -1
}

# macOS/Linux 的 `ps -o comm=` 有的会给完整路径、有的只给程序名，统一取
# basename 再比较，避免因为调用路径不同（相对路径 vs 绝对路径）导致误判。
proc_comm() {
    local pid="$1"
    [[ -n "$pid" ]] || return 1
    local comm
    comm="$(ps -p "$pid" -o comm= 2>/dev/null | tail -1)"
    [[ -n "$comm" ]] && basename "$comm"
}

is_our_pid() {
    local pid="$1"
    [[ -n "$pid" ]] || return 1
    [[ "$(proc_comm "$pid")" == "$BIN_NAME" ]]
}

# 每次判活顺便把 run.pid 同步成端口真正的属主 PID，这样后续 stop/status
# 操作的对象永远是"实际监听端口的那个进程"，不会出现 run.pid 记录的 PID
# 和真正在跑的进程对不上的情况。只有确认是我们自己的进程才写入/保留，
# 否则清空，避免 run.pid 里存着别的程序的 PID。
sync_pid_file() {
    local pid="$1"
    if [[ -n "$pid" ]] && is_our_pid "$pid"; then
        echo "$pid" >"$PID_FILE"
    else
        rm -f "$PID_FILE"
    fi
}

is_running() {
    local pid
    pid="$(port_pid)"
    sync_pid_file "$pid"
    is_our_pid "$pid"
}

# 端口被占用、但占用者不是我们自己编译出来的那个二进制——这种情况下不能
# 当成"服务已经在运行"直接跳过，也不能硬启动（启动了也绑不上端口），
# 要明确报错让人去处理端口冲突，而不是让 go build 之后的 bind 失败才发现。
port_conflict() {
    local pid
    pid="$(port_pid)"
    [[ -n "$pid" ]] && ! is_our_pid "$pid"
}

start() {
    if is_running; then
        echo "服务已在运行（端口 $PORT 已被占用），PID $(cat "$PID_FILE")"
        return 0
    fi

    if port_conflict; then
        local pid
        pid="$(port_pid)"
        echo "端口 ${PORT} 已被其他程序占用（PID ${pid}，程序名 $(proc_comm "$pid")），拒绝启动。请先停止占用该端口的程序，或修改 config/config.yml 里的 port 配置"
        exit 1
    fi

    build
    mkdir -p "$LOG_DIR"

    nohup "$BIN_PATH" -config "$CONFIG_PATH" >>"$STDOUT_LOG" 2>&1 &
    local started_pid=$!

    # 端口判活比原来的"进程存在即算启动成功"要晚：进程起来后还要经过字典
    # 加载、migration 等初始化步骤才会真正 listen，固定 sleep 1 等不到，
    # 轮询到端口出现或者进程已退出为止，避免把"还在初始化"误判成"启动失败"。
    local waited=0
    while (( waited < 20 )); do
        if ! kill -0 "$started_pid" 2>/dev/null; then
            break
        fi
        if [[ -n "$(port_pid)" ]]; then
            break
        fi
        sleep 1
        waited=$((waited + 1))
    done

    if is_running; then
        local actual_pid
        actual_pid="$(cat "$PID_FILE")"
        if [[ "$actual_pid" != "$started_pid" ]]; then
            echo "警告：监听端口 $PORT 的进程 PID $actual_pid 与刚启动的进程 PID $started_pid 不一致，请检查是否有其他实例"
        fi
        echo "服务已启动，PID ${actual_pid}，日志：$STDOUT_LOG"
    else
        echo "服务启动失败，请查看日志：$STDOUT_LOG"
        rm -f "$PID_FILE"
        exit 1
    fi
}

stop() {
    if ! is_running; then
        echo "服务未在运行"
        rm -f "$PID_FILE"
        return 0
    fi

    local pid
    pid="$(cat "$PID_FILE")"
    echo "停止服务，PID $pid ..."
    kill "$pid"

    for _ in $(seq 1 10); do
        kill -0 "$pid" 2>/dev/null || break
        sleep 1
    done

    if kill -0 "$pid" 2>/dev/null; then
        echo "服务未响应，强制终止"
        kill -9 "$pid"
    fi

    rm -f "$PID_FILE"
    echo "服务已停止"
}

status() {
    if is_running; then
        echo "服务运行中，PID $(cat "$PID_FILE")（端口 ${PORT}）"
    else
        echo "服务未运行"
    fi
}

case "${1:-}" in
    start)
        start
        ;;
    stop)
        stop
        ;;
    restart)
        stop
        start
        ;;
    status)
        status
        ;;
    *)
        echo "用法：$0 {start|stop|restart|status}"
        exit 1
        ;;
esac
