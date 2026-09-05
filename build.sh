#!/usr/bin/env bash
# Wiki-Brain 跨平台构建脚本
# 用法：./build.sh [version] [--target OS/ARCH]...
#       ./build.sh [--version VERSION] [--target OS/ARCH]...
#       ./build.sh --all [version]
#
# 默认产出 darwin/arm64（Apple Silicon）和 windows/amd64。指定一个或多个
# --target 后只构建所指定的目标；--all 构建全部支持的目标。
#
# 依赖已从 mattn/go-sqlite3（CGO）换成 modernc.org/sqlite（纯 Go 实现，见
# internal/foundation/db/db.go），因此这里全程 CGO_ENABLED=0，不需要给每个
# 目标平台准备对应的 C 交叉编译工具链（mingw-w64 / osxcross 等）。
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

DEFAULT_VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
VERSION=""
DIST_DIR="$ROOT_DIR/dist"
BIN_NAME="wiki-brain-server"
PKG="./cmd/server"

# 可构建的 GOOS/GOARCH 组合，与 `go tool dist list` 的命名一致。
ALL_TARGETS=(
    "linux amd64"
    "linux arm64"
    "darwin amd64"
    "darwin arm64"
    "windows amd64"
    "windows arm64"
)

# 默认仅构建面向当前发布渠道的两个安装包。
TARGETS=(
    "darwin arm64"
    "windows amd64"
)

usage() {
    cat <<'EOF'
用法：./build.sh [version] [--target OS/ARCH]...
      ./build.sh [--version VERSION] [--target OS/ARCH]...
      ./build.sh --all [version]

默认目标：darwin/arm64、windows/amd64
可选目标：linux/amd64、linux/arm64、darwin/amd64、darwin/arm64、windows/amd64、windows/arm64

示例：
  ./build.sh v1.2.3
  ./build.sh --target linux/amd64 --target darwin/amd64
  ./build.sh v1.2.3 --all
EOF
}

selected_targets=()
build_all=false
while [[ $# -gt 0 ]]; do
    case "$1" in
        --target)
            [[ $# -ge 2 ]] || { echo "--target 需要 OS/ARCH 参数" >&2; exit 1; }
            selected_targets+=("${2//\// }")
            shift 2
            ;;
        --version)
            [[ $# -ge 2 ]] || { echo "--version 需要版本号参数" >&2; exit 1; }
            VERSION="$2"
            shift 2
            ;;
        --all)
            build_all=true
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        -*)
            echo "未知参数：$1" >&2
            usage >&2
            exit 1
            ;;
        *)
            [[ -z "$VERSION" ]] || { echo "版本号只能指定一次" >&2; exit 1; }
            VERSION="$1"
            shift
            ;;
    esac
done

VERSION="${VERSION:-$DEFAULT_VERSION}"

if [[ "$build_all" == true && ${#selected_targets[@]} -gt 0 ]]; then
    echo "--all 不能与 --target 同时使用" >&2
    exit 1
fi
if [[ "$build_all" == true ]]; then
    TARGETS=("${ALL_TARGETS[@]}")
elif [[ ${#selected_targets[@]} -gt 0 ]]; then
    TARGETS=("${selected_targets[@]}")
fi

for target in "${TARGETS[@]}"; do
    valid=false
    for supported in "${ALL_TARGETS[@]}"; do
        [[ "$target" == "$supported" ]] && valid=true && break
    done
    if [[ "$valid" == false ]]; then
        echo "不支持的构建目标：${target/ /\/}" >&2
        usage >&2
        exit 1
    fi
done

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
    # 但 config/ 和预制知识领域 preset/ 是外部运行时数据，不在二进制中，需随包分发。
    cp -R "$ROOT_DIR/config" "$out_dir/config"
    cp -R "$ROOT_DIR/preset" "$out_dir/preset"
    mkdir -p "$out_dir/logs"
    cp "$ROOT_DIR/scripts/README.txt" "$out_dir/README.txt"

    # Windows 产物额外带上 NSSM 服务管理脚本（把普通控制台程序注册/管理为
    # Windows 服务），依赖用户自备 nssm.exe，见脚本内说明。
    if [[ "$goos" == "windows" ]]; then
        cp "$ROOT_DIR/scripts/windows/service.ps1" "$out_dir/service.ps1"
        cp "$ROOT_DIR/scripts/windows/service.bat" "$out_dir/service.bat"
    else
        # linux/darwin 产物带上启动/停止脚本（tar.gz 保留可执行权限）。
        cp "$ROOT_DIR/scripts/unix/run.sh" "$out_dir/run.sh"
        chmod +x "$out_dir/run.sh"
    fi

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
