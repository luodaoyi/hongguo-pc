#!/usr/bin/env bash
# scripts/package.sh —— 把 bin/ 里的产物打成发布包
#
# 命名统一为：hongguo-pc_<os>_<arch>.tar.gz | .zip
#   os   : linux | windows | macos
#   arch : amd64 | arm64
#
# 用法：
#   GOOS=linux  GOARCH=amd64 bash scripts/package.sh
#   GOOS=darwin GOARCH=arm64 bash scripts/package.sh
#
# 设计说明：
# - Linux 发 tar.gz 而不是 deb/rpm/AppImage：后者依赖 linuxdeploy 联网下载，
#   在 CI 上是不稳定源；运行时依赖在 README 里写明。
# - macOS 用 ditto 打包 .app（保留 bundle 结构与可执行位），裸 zip 会破坏签名/权限。
# - Windows 优先 7z，退回 Compress-Archive，再退回 zip。
set -euo pipefail

APP_NAME="${APP_NAME:-hongguo-pc}"
GOOS="${GOOS:-$(go env GOOS)}"
GOARCH="${GOARCH:-$(go env GOARCH)}"
OUT_DIR="${OUT_DIR:-bin}"
DIST_DIR="${DIST_DIR:-dist}"

die() { echo "error: $*" >&2; exit 1; }
note() { echo "==> $*"; }

mkdir -p "$DIST_DIR"

case "$GOOS" in
  linux)
    [ -d "$OUT_DIR" ] || die "missing $OUT_DIR"
    ARTIFACT="$DIST_DIR/${APP_NAME}_linux_${GOARCH}.tar.gz"
    note "package: $ARTIFACT"
    tar -czf "$ARTIFACT" -C "$OUT_DIR" --exclude='*.app' .
    ;;

  windows)
    ARTIFACT="$DIST_DIR/${APP_NAME}_windows_${GOARCH}.zip"
    note "package: $ARTIFACT"
    rm -f "$ARTIFACT"
    if command -v 7z >/dev/null 2>&1; then
      7z a -tzip "$ARTIFACT" "./$OUT_DIR"/*.exe >/dev/null
    elif command -v zip >/dev/null 2>&1; then
      (cd "$OUT_DIR" && zip -q -r "../$ARTIFACT" ./*.exe)
    elif command -v powershell >/dev/null 2>&1 || command -v pwsh >/dev/null 2>&1; then
      PS=$(command -v pwsh 2>/dev/null || command -v powershell)
      "$PS" -NoProfile -Command \
        "Compress-Archive -Path '$OUT_DIR/*.exe' -DestinationPath '$ARTIFACT' -Force"
    else
      die "no zip tool found (7z / zip / powershell)"
    fi
    ;;

  darwin)
    APP_DIR="$OUT_DIR/$APP_NAME.app"
    [ -d "$APP_DIR" ] || die "missing $APP_DIR（先跑 scripts/build.sh --app，或用 wails3 package）"
    ARTIFACT="$DIST_DIR/${APP_NAME}_macos_${GOARCH}.zip"
    note "package: $ARTIFACT"
    rm -f "$ARTIFACT"
    if command -v ditto >/dev/null 2>&1; then
      ditto -c -k --sequesterRsrc --keepParent "$APP_DIR" "$ARTIFACT"
    elif command -v zip >/dev/null 2>&1; then
      # 退回 zip：必须在 .app 的父目录执行，且用 -y 保留符号链接
      (cd "$OUT_DIR" && zip -q -y -r "../$ARTIFACT" "$APP_NAME.app")
    else
      die "no zip tool found (ditto / zip)"
    fi
    ;;

  *)
    die "unsupported GOOS: $GOOS"
    ;;
esac

[ -f "$ARTIFACT" ] || die "packaging produced no artifact"
note "done: $ARTIFACT ($(wc -c < "$ARTIFACT") bytes)"
