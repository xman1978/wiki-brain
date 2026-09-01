#!/usr/bin/env bash
# Wiki-Brain 跨平台构建脚本
# 用法：./build.sh [version]
#
# 产出 logs/dist（见下方 DIST_DIR）下的 6 个平台组合的可执行文件：
#   linux/amd64、linux/arm64、darwin/amd64（Intel Mac）、darwin/arm64（Apple
#   Silicon）、windows/amd64、windows/arm64。
#
# 依赖已从 mattn/go-sqlite3（CGO）换成 modernc.org/sqlite（纯 Go 实现，见
# internal/foundation/db/db.go），因此这里全程 CGO_ENABLED=0，不需要给每个
# 目标平台准备对应的 C 交叉编译工具链（mingw-w64 / osxcross 等）。
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
DIST_DIR="$ROOT_DIR/dist"
BIN_NAME="wiki-brain-server"
PKG="./cmd/server"

# GOOS/GOARCH 组合，与 `go tool dist list` 的命名一致
TARGETS=(
    "linux amd64"
    "linux arm64"
    "darwin amd64"
    "darwin arm64"
    "windows amd64"
    "windows arm64"
)

echo "构建版本：${VERSION}"
rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

for target in "${TARGETS[@]}"; do
    read -r goos goarch <<<"$target"

    ext=""
    [[ "$goos" == "windows" ]] && ext=".exe"

    out_dir="$DIST_DIR/${goos}-${goarch}"
    out_path="${out_dir}/${BIN_NAME}${ext}"
    mkdir -p "$out_dir"

    echo "==> ${goos}/${goarch}"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
        go build -trimpath -ldflags "-s -w" -o "$out_path" "$PKG"

    # 服务运行还依赖 config/、preset/、web 静态资源已通过 go:embed 打进二进制，
    # 但 config/ 是外部可编辑的运行时配置（prompts、词典、preset 数据），不在
    # 编译产物里，需要随包分发。
    cp -R "$ROOT_DIR/config" "$out_dir/config"
    mkdir -p "$out_dir/logs"

    archive_base="wiki-brain-${VERSION}-${goos}-${goarch}"
    (
        cd "$DIST_DIR"
        if [[ "$goos" == "windows" ]]; then
            zip -rq "${archive_base}.zip" "${goos}-${goarch}"
        else
            tar -czf "${archive_base}.tar.gz" "${goos}-${goarch}"
        fi
    )
done

echo
echo "完成，产物在 ${DIST_DIR}："
ls -lh "$DIST_DIR"/*.zip "$DIST_DIR"/*.tar.gz 2>/dev/null
