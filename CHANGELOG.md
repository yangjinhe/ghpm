# Changelog

## Unreleased — macOS support

### 新增

- **macOS 构建与发布**：新增 darwin/amd64、darwin/arm64 的 release 包；
  CI 增加 macos-latest 作业，单元测试在 Linux 与 macOS 上同时运行。
- **平台路径**：macOS 使用 `~/Library/Application Support/ghpm`（配置与数据）、
  `~/Library/Caches/ghpm`、`~/Library/Logs/ghpm`；软链仍在 `~/.local/bin`。
  显式设置 `XDG_*` 时仍然优先。
- **资产匹配**：识别 `macos`/`osx`/`apple-darwin`、`x86_64h`、`arm64e`、
  `universal`/`universal2`；`.pkg` 纳入可安装格式，`.dmg` 在评分中明确排除
  （`ghpm assets` 仍会显示）。
- **install.type pkg**：macOS 上调用 `installer -pkg ... -target /`，
  与 deb/rpm 共用包缓存和 sudo 策略；非 macOS 上给出明确错误。
- **通知**：macOS 使用 `osascript display notification`；新增 launchd 模板
  `contrib/launchd/io.github.ghpm.check.plist`。
- **归档清理**：解包和主二进制选择会忽略 `__MACOSX/`、`._*`、`.DS_Store`
  等 AppleDouble/Finder 元数据。
- **Gatekeeper**：安装、升级和回滚后会递归清除 `com.apple.quarantine`
  （darwin 下使用原生 `Removexattr`/`Lremovexattr`，其他平台为空操作），
  避免下载的程序被 macOS 拒绝执行；清理失败会作为 warning 返回。

### 变更

- 测试夹具改为按 `runtime.GOOS/GOARCH` 生成资产名，便于在 macOS 上运行同一套测试。
- 版本号开发标记提升到 0.3.0-dev。

## v0.2.0 (2026-11-17)

界面、供应链与工程化。

### 新增

- **本地 Web 界面**：`ghpm web` 提供列表、筛选、批量安装/升级、添加、
  固定、回滚、移除；下载与安装进度通过 SSE 实时推送。默认只监听
  `127.0.0.1:8787`，校验 Host 头并强制 JSON 写入，静态资源内嵌在二进制中。
- **清单签名校验**：可为每个应用配置 `signature`，校验 checksum 清单的
  分离签名。minisign 使用原生 Ed25519/BLAKE2b 实现；OpenPGP 交给系统
  `gpg`，支持公钥文件、内联公钥、指纹和临时 keyring。签名通过后才信任
  清单里的 SHA-256；若 GitHub API 同时提供 digest，还会交叉核对。
- **GraphQL 批量检查**：有 token 且应用多于一个时，`check`/`update` 用
  一次 GraphQL 请求查询 50 个仓库的最新 tag，失败时自动回退 REST。
- **man page 生成**：`make docs` / `ghpm doc`，发布包内附带手册页。
- **CLI 集成测试**：覆盖 install → list → update → pin → rollback → remove
  的完整流程，并新增 Web API/SSE、签名校验、配置往返等测试。

### 变更

- `ghpm update --json` 不再把人类可读的升级计划混进 JSON 输出。
- GitHub 客户端的 User-Agent 改为按实例保存，消除全局变量在并发检查下的
  数据竞争。
- README 与示例配置补充 Web、签名、批量检查与新目录结构。

### 修复

- 签名校验失败会删除缓存并在安装前中止，不会留下半成品软链。
- 签名与 checksum 清单缺失时，`required` 模式给出明确错误，`if-available`
  仅告警。

## v0.1.3 (2026-11-17)

- `add`/`install`/`update` 强制重新校验 release 元数据（条件请求，
  304 不消耗配额），刚发布的 release 立即可见。

## v0.1.2 (2026-11-17)

- 多可执行文件归档按执行位、扩展名、目录深度和 app/asset 提示选主二进制，
  忽略文档与 helper 脚本。

## v0.1.1 (2026-11-17)

- 私有仓库下载鉴权、桌面通知、systemd timer 模板与发布工作流。

## v0.1.0 (2026-11-17)

- 首个可用版本：配置、资产匹配、下载校验、原子安装、更新/回滚/pin、
  TUI、私有仓库与镜像前缀。
