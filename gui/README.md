# ghpm-gui — MyGo 原生桌面界面

`ghpm-gui` 是 ghpm 的桌面前端，使用 [MyGo](https://github.com/egoist/mygo)
的原生 UI（`ui` 包）实现：纯 Go、无 cgo、不加载 WebView，与 CLI/TUI/Web
共用 `internal/app` 服务和 SQLite 状态。

## 构建与运行

要求 Go 1.27.3+（模块声明会通过 Go 工具链自动下载）：

```bash
make gui          # 仓库根目录，生成 ./ghpm-gui
./ghpm-gui

# 或者
cd gui && go build -o ../ghpm-gui .
```

## 测试

```bash
cd gui
go test ./...

# 无窗口渲染一张主界面截图
GHPM_GUI_SNAPSHOT=/tmp/ghpm-gui.png go test -run TestSnapshot -v
# 可选：GHPM_GUI_SNAPSHOT_MODE=add|details|progress
```

`ui.NewTester` 在内存中渲染视图，测试不依赖真实窗口；`backend` 接口让
测试注入假服务，不触碰网络或用户的 state.db。

## 结构

| 文件 | 内容 |
|---|---|
| `main.go` | 应用启动、菜单、单实例、窗口 |
| `backend.go` | `internal/app.Service` 的窄接口与实现 |
| `controller.go` | 状态、异步任务、进度事件、所有操作 |
| `view.go` | 原生 UI 布局、表格、弹窗、进度面板 |
| `status.go` | 状态文案与结果汇总（可单测） |

线程模型：视图与菜单在主线程；耗时操作在 goroutine 中执行，进度事件通过
`Window.Update` 回到主线程并触发重绘。

## 发布通道

GUI 使用独立 tag（如 `v0.5.0-gui`）发布，`make_latest: false`，不会覆盖
CLI 的 latest release，也不会与 CLI 资产混放。工作流会：

- macOS runner：`mygo build -platform darwin/universal`，发布 dmg
- Linux runner：`mygo build -platform linux/amd64,linux/arm64`，发布 tar.gz 与 deb
- 从 `gui/CHANGELOG.md` 生成本次 release 说明

安装：

```bash
ghpm add yangjinhe/ghpm --channel tag:v0.5.0-gui --id ghpm-gui --install
```

macOS 会识别 dmg 中的 `ghpm-gui.app` 并软链到 `/Applications`；Linux 会安装
`ghpm-gui` 二进制到 `~/.local/bin`。

频道固定为 `tag:vX-gui`，所以 `ghpm check` 不会自动发现新的 GUI 版本；
升级时重新执行 `ghpm install ghpm-gui --tag <新 tag>` 或重新 add。

## 手动打包

`mygo.json` 与 `resources/icon.png` 已就绪，可在 macOS 上生成 `.app`/dmg，
或交叉编译其他平台：

```bash
cd gui
go run github.com/egoist/mygo/cmd/mygo@v0.10.0 build \
  -platform darwin/universal,linux/amd64,linux/arm64
```

macOS 的签名/公证、Windows 安装器（NSIS）需要相应平台的工具；正式发布前
还需决定 `ghpm-gui` 与核心 CLI 的版本/更新策略，避免两套自动更新同时存在。
