# Changelog

## Unreleased

### 变更

- **安装元数据迁移到 SQLite**：`state.json` 改为 `state.db`（SQLite 单文件 +
  busy_timeout，纯 Go 驱动，保持 CGO-free 交叉编译）。首次启动自动导入旧
  JSON 的全部字段，成功后删除 `state.json` 与 `state.json.lock`；JSON 损坏
  时中止并保留文件，修复后可重试。数据库位于数据目录，支持并发 CLI/TUI/Web
  访问。

### 修复

- **命令行参数统一**：`install`/`check`/`update`/`remove`/`pin`/`rollback`/
  `prune` 现在都接受 app id、`owner/repo` 或 GitHub URL。
  `ghpm remove Minireel/HGhub` 会被解析到配置里的 id。
- **Web 进度面板无法关闭**：`.drawer { display:flex }` 覆盖了浏览器对
  `[hidden]` 的默认样式，导致右上角 × 无效。新增全局
  `[hidden] { display:none !important }`，并补充回归测试。
- **universal 资产映射**：`ghpm add` 之前会把 darwin 平台映射到架构专属的
  `.dmg`，即使 release 里同时有评分更高的 universal `.app.tar.gz`。现在胖
  二进制会注册到每个支持的架构。
- **dmg 资产路由**：旧配置里 `install.type: archive` 配上 `.dmg` 资产时会
  错误地尝试解包；安装器现在按资产后缀路由到 DMG 流程。

## v0.3.1 (2026-11-18)

macOS 应用包与 DMG 支持。

### 新增

- **DMG 支持**：只有 `.dmg` 的 release 也可安装。macOS 上只读挂载镜像、
  用 `ditto` 取出 `.app`（保留执行位、符号链接与签名数据）后 detach；
  release 同时提供 zip/tar.gz 时优先归档。镜像内没有 `.app`、或不在
  macOS 上执行 dmg 安装时会给出明确错误。
- **自动识别 `.app`**：归档里只有 macOS 应用包（如 `Motty.app`）时，安装为
  application bundle 而不是把 `Contents/MacOS` 里的可执行文件软链到
  `~/.local/bin`。版本保存在数据目录，`/Applications` 中创建指向当前版本的
  软链；升级和回滚原子切换软链，`ghpm remove` 会一并删除应用链接。
  `/Applications` 不可写时回退到 `~/Applications` 并给出 warning。
- **`install.type: app`**：可显式要求应用包安装；归档同时包含 CLI 与 `.app`
  时仍默认安装 CLI，设置 `install.bin` 可强制选择包内可执行文件。
- **应用目录配置**：`defaults.apps_dir` / `GHPM_APPS_DIR`（macOS 默认
  `/Applications`）；`ghpm doctor` 会显示。
- **符号链接解包**：保留归档内的相对符号链接（Electron 等应用包需要），
  绝对路径、越界目标以及通过符号链接写入后续条目的归档一律拒绝。
- **bundle 可执行位修复**：ZIP 丢失执行位时恢复 `Contents/MacOS/*` 的 0755，
  避免 LaunchServices 无法启动。

### 变更

- `ghpm rollback` 在旧版本 bundle 名不同时通过 `.app` 探测定位，而不是退回
  包内裸可执行文件。

## v0.3.0 (2026-11-18)

macOS 支持与 Gatekeeper 兼容。

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
