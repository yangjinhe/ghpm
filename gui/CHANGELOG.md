# ghpm-gui changelog

## v0.5.0-gui (2026-11-19)

首个原生桌面界面版本，使用 MyGo 原生 UI（纯 Go、无 cgo、无 WebView）：

- 应用表格、搜索过滤、检查更新、全部更新
- 添加应用、安装/升级、pin、回滚、详情、卸载（保留或删除配置）
- 实时下载/检查进度面板
- 原生菜单、桌面通知、单实例
- macOS 发布 universal dmg；Linux 发布 tar.gz 与 deb，原生 UI 不依赖 WebKitGTK
- 与 CLI/TUI/Web 共用 `internal/app` 和 SQLite 状态
