#!/usr/bin/env bash
# Wiki-Brain 服务启动/停止脚本
# 用法：./run.sh start | ./run.sh stop | ./run.sh restart | ./run.sh status

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

BIN_DIR="$ROOT_DIR/bin"
BIN_PATH="$BIN_DIR/wiki-brain-server"
CONFIG_PATH="$ROOT_DIR/config/config.yml"
PID_FILE="$ROOT_DIR/run.pid"
LOG_DIR="$ROOT_DIR/logs"
STDOUT_LOG="$LOG_DIR/server.out.log"

build() {
    mkdir -p "$BIN_DIR"
    echo "构建中..."
    go build -o "$BIN_PATH" ./cmd/server
}

is_running() {
    [[ -f "$PID_FILE" ]] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null
}

start() {
    if is_running; then
        echo "服务已在运行，PID $(cat "$PID_FILE")"
        return 0
    fi

    build
    mkdir -p "$LOG_DIR"

    nohup "$BIN_PATH" -config "$CONFIG_PATH" >>"$STDOUT_LOG" 2>&1 &
    echo $! >"$PID_FILE"
    sleep 1

    if is_running; then
        echo "服务已启动，PID $(cat "$PID_FILE")，日志：$STDOUT_LOG"
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
        echo "服务运行中，PID $(cat "$PID_FILE")"
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
