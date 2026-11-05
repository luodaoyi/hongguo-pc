// Package frontend 把前端构建产物（frontend/dist）嵌入二进制。
//
// 为什么放在 frontend/ 目录：go:embed 的路径相对本文件所在目录解析，且不允许
// `..`；把包放在产物旁边是唯一能让 `all:dist` 成立的位置。dist 由
// `npm run build` 生成，必须先跑前端构建再 go build（缺失即编译期报错，
// 不再是运行期找不到目录）。
//
// 背景见 docs/rootcause-exe-not-launch.md（主因：dist 从未随发布包分发）。
package frontend

import "embed"

// Dist 是前端构建产物的只读文件系统。交给
// application.AssetFileServerFS 使用；wails 会在其中自动定位 index.html
// 并以该目录为根对外服务。
//
// all: 前缀不可省——否则以 `_` / `.` 开头的文件会被静默跳过。
//
//go:embed all:dist
var Dist embed.FS
