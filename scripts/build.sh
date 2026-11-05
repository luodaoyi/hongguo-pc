#!/usr/bin/env bash
# scripts/build.sh —— 平台构建脚本（CI 与本地共用）
#
# 用途：在「没有 wails3 生成的 build/ 资产」时也能产出可运行的二进制。
# 若仓库里存在 build/Taskfile.yml（即标准的 wails3 项目骨架），CI 会优先走
# wails3 build / wails3 package，本脚本作为可控的兜底路径。
#
# 用法：
#   GOOS=linux   GOARCH=amd64 bash scripts/build.sh
#   GOOS=darwin  GOARCH=arm64 bash scripts/build.sh --app
#   bash scripts/build.sh --help
#
# 环境变量：
#   APP_NAME             产物名，默认 hongguo-pc
#   GOOS / GOARCH        目标平台，默认取 go env
#   ENTRY                入口包，默认 ./cmd/app
#   OUT_DIR              输出目录，默认 bin
#   SKIP_FRONTEND_BUILD  设为 1 时跳过 npm run build（CI 已单独构建前端）
#   ALLOW_NO_FRONTEND    设为 1 时允许 frontend/ 缺失（仅用于脚手架期）
set -euo pipefail

APP_NAME="${APP_NAME:-hongguo-pc}"
GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"
ENTRY="${ENTRY:-./cmd/app}"
OUT_DIR="${OUT_DIR:-bin}"
MAKE_APP=0

for arg in "$@"; do
  case "$arg" in
    --app) MAKE_APP=1 ;;
    --help|-h) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "unknown argument: $arg" >&2; exit 2 ;;
  esac
done

die() { echo "error: $*" >&2; exit 1; }
note() { echo "==> $*"; }

# ---------------------------------------------------------------- 0. 前置检查
[ -f go.mod ] || die "not in repository root (go.mod missing)"
[ -d "$ENTRY" ] || die "entry package not found: $ENTRY（应用入口尚未落地）"
command -v go >/dev/null 2>&1 || die "go not found in PATH"

# ---------------------------------------------------------------- 1. 前端
if [ -f frontend/package.json ]; then
  if [ "${SKIP_FRONTEND_BUILD:-0}" = "1" ] && [ -d frontend/dist ]; then
    note "frontend: skip (SKIP_FRONTEND_BUILD=1, dist/ 已存在)"
  else
    command -v npm >/dev/null 2>&1 || die "npm not found in PATH (frontend build required)"
    note "frontend: install + build"
    (
      cd frontend
      if [ -f package-lock.json ]; then npm ci; else npm install; fi
      npm run build
    )
  fi
  [ -d frontend/dist ] || die "frontend build finished but frontend/dist is missing"
else
  if [ "${ALLOW_NO_FRONTEND:-0}" = "1" ]; then
    note "frontend: package.json 缺失，按 ALLOW_NO_FRONTEND=1 继续"
  else
    die "frontend/package.json 缺失（脚手架期可设 ALLOW_NO_FRONTEND=1 临时跳过）"
  fi
fi

# ---------------------------------------------------------------- 2. 绑定
# 优先让 wails3 负责；失败不静默：前端若真依赖 bindings，会在 tsc/vite 阶段报错。
if command -v wails3 >/dev/null 2>&1; then
  note "bindings: wails3 generate bindings"
  if ! wails3 generate bindings; then
    echo "warning: wails3 generate bindings 失败，继续（前端构建会暴露缺绑定问题）" >&2
  fi
else
  note "bindings: 跳过（本机无 wails3）"
fi

# ---------------------------------------------------------------- 3. go build
EXT=""
LDFLAGS="-s -w"
case "$GOOS" in
  windows) EXT=".exe"; LDFLAGS="-s -w -H windowsgui" ;;
esac

# CGO：Windows 默认不需要（走纯 Go 交叉编译）；Linux/macOS 的 WebView 集成需要。
CGO="${CGO_ENABLED:-}"
if [ -z "$CGO" ]; then
  case "$GOOS" in
    windows) CGO=0 ;;
    *) CGO=1 ;;
  esac
fi

mkdir -p "$OUT_DIR"
BIN_PATH="$OUT_DIR/$APP_NAME$EXT"
note "go build: GOOS=$GOOS GOARCH=$GOARCH CGO_ENABLED=$CGO -> $BIN_PATH"
CGO_ENABLED="$CGO" GOOS="$GOOS" GOARCH="$GOARCH" \
  go build -trimpath -tags production -ldflags="$LDFLAGS" -o "$BIN_PATH" "$ENTRY"
[ -f "$BIN_PATH" ] || die "build produced no binary at $BIN_PATH"

# ---------------------------------------------------------------- 4. macOS .app
# 仅当没有 wails3 生成的 .app 时才自己拼一个最小 bundle。
if [ "$MAKE_APP" = "1" ]; then
  [ "$GOOS" = "darwin" ] || die "--app 只用于 darwin 目标"
  APP_DIR="$OUT_DIR/$APP_NAME.app"
  if [ -d "$APP_DIR" ]; then
    note "app bundle: 已存在（wails3 产物），不再重建"
  else
    note "app bundle: 生成最小 $APP_DIR"
    mkdir -p "$APP_DIR/Contents/MacOS" "$APP_DIR/Contents/Resources"
    cp "$BIN_PATH" "$APP_DIR/Contents/MacOS/$APP_NAME"
    chmod +x "$APP_DIR/Contents/MacOS/$APP_NAME"
    cat > "$APP_DIR/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>${APP_NAME}</string>
  <key>CFBundleDisplayName</key><string>${APP_NAME}</string>
  <key>CFBundleExecutable</key><string>${APP_NAME}</string>
  <key>CFBundleIdentifier</key><string>com.luodaoyi.hongguo-pc</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>${APP_VERSION:-0.1.0}</string>
  <key>CFBundleVersion</key><string>${APP_VERSION:-0.1.0}</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST
  fi
fi

note "done: $BIN_PATH"
