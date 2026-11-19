# ghpm — GitHub Release 软件管理器

`ghpm` 把「GitHub Release 里散装发布的二进制」变成可管理的软件包：

> 配置一个仓库 → 自动识别架构/平台、选中正确的 release 资产 → 下载并校验 →
> 安装到用户目录 → 之后一条命令检查更新、选择升级、随时回滚。

面向 **Linux + macOS、用户级安装**：默认不调用 `sudo`，不污染系统目录。
Linux 上配置与状态在 `~/.config`、`~/.local`；macOS 上遵循系统惯例使用
`~/Library`，软链仍在 `~/.local/bin`。

```
┌ ghpm ── linux/amd64 ─────────────── 5 apps · 2 updates ┐
│  ▸ ● lazygit     0.44.1 → 0.45.0        update         │
│    ○ ripgrep     14.1.1                 up to date     │
│    ○ fd          10.2.0 → 10.3.0        update         │
│    ○ bottom      0.9.6                  pinned         │
├─────────────────────────────────────────────────────────┤
│  ⠋ lazygit ████████████░░░░ 62% 3.1 MiB/s               │
├─────────────────────────────────────────────────────────┤
│ space 选择 · i 安装 · u 升级 · a 添加 · c 检查 · q 退出 │
└─────────────────────────────────────────────────────────┘
```

## 特性

- **自动平台/架构匹配**：识别 `x86_64` / `amd64` / `aarch64` / `armv7l` /
  `i686` / `loongarch64` / `riscv64` 等几十种写法，过滤校验文件、签名、
  源码包、debug 构建、其他 OS 的产物。
- **三种匹配配置**：按 `os/arch` 精确映射、模板占位符、正则兜底；`ghpm add`
  会根据 release 自动生成多平台映射。
- **校验**：优先使用 GitHub API 的 `digest`，其次自动下载
  `checksums.txt` / `SHA256SUMS` / `*.sha256` 并核对 SHA-256。
- **原子安装与升级**：下载到 `.part` 断点续传 → 解包到临时目录 → 原子
  rename → 原子切换软链。升级失败不会破坏旧版本。
- **版本保留与回滚**：默认保留 3 个版本，`ghpm rollback` 一条命令切回。
- **更新检查**：ETag 条件请求（304 不计配额）、元数据缓存、并发检查、
  限流感知；有 token 时自动改用 GraphQL 批量查询（50 个仓库/请求），
  大幅降低配额消耗；支持固定版本（pin）。
- **签名校验**：可选校验 checksum 清单的分离签名——minisign 用原生
  Ed25519/BLAKE2b 实现，OpenPGP 交给系统 `gpg`；校验通过后才信任
  清单里的 SHA-256，形成“签名 → 校验和 → 资产”的完整链路。
- **私有仓库**：设置 `GITHUB_TOKEN` 后，API 查询与资产下载都会携带鉴权。
- **中文网络友好**：支持 `proxy` 配置（HTTP/SOCKS5）和下载镜像前缀
  （如 `https://ghfast.top`），镜像失败自动回退直连。
- **TUI + CLI + Web**：命令行、bubbletea 终端界面和本地浏览器界面共用
  同一套服务层，行为一致。

## 构建

需要 Go 1.25+（`go.mod` 已声明；旧版 Go 可通过 `GOTOOLCHAIN=auto` 自动下载）。

```bash
cd /home/yangjh/aiproject
make build          # 生成 ./ghpm
make test           # 单元 + 离线集成测试
sudo make install   # 安装到 /usr/local/bin（可选）
make install PREFIX=$HOME/.local   # macOS 上更常见的用户级安装
```

macOS 也可以直接交叉编译或使用 release 里的 `darwin_arm64`/`darwin_amd64`
包；`go test ./...` 在 macOS 上同样通过（CI 有独立的 macos-latest 任务）。

或者：

```bash
go build -o ghpm ./cmd/ghpm
```

Release 页面提供 Linux 的 `tar.gz`/`.deb` 与 macOS 的 `tar.gz`（以下用
`<version>` 代表实际版本号）：

```bash
sudo dpkg -i ghpm_<version>_amd64.deb   # Debian/Ubuntu，安装到 /usr/bin，附带 man page
tar -xzf "ghpm_<version>_linux_amd64.tar.gz" && "./ghpm_<version>_linux_amd64/ghpm" --help
tar -xzf "ghpm_<version>_darwin_arm64.tar.gz" && "./ghpm_<version>_darwin_arm64/ghpm" --help
```

## 快速开始

```bash
# 添加并立即安装（默认装到 ~/.local/bin）
./ghpm add jesseduffield/lazygit --install

# 也可以只配置，稍后安装
./ghpm add sharkdp/fd
./ghpm add BurntSushi/ripgrep --arch arm64 --os linux   # 为另一台机器准备配置
./ghpm install

# 查看状态 / 检查更新 / 升级
./ghpm list
./ghpm check
./ghpm update            # 检查后交互确认
./ghpm update --yes

# 回滚、固定、卸载
./ghpm rollback lazygit 0.44.1
./ghpm pin lazygit
./ghpm remove lazygit --purge

# 交互界面
./ghpm tui
./ghpm web --open        # http://127.0.0.1:8787
./ghpm-gui               # 原生桌面界面（make gui）
```

> 如果 `~/.local/bin` 不在 `PATH` 里，`ghpm doctor` 会提示；把它加进
> `~/.bashrc` 或 `~/.zshrc`：`export PATH="$HOME/.local/bin:$PATH"`。

### macOS

- 配置与数据在 `~/Library/Application Support/ghpm`，缓存在
  `~/Library/Caches/ghpm`，日志目录在 `~/Library/Logs/ghpm`；
  可执行软链仍在 `~/.local/bin`。
- 资产匹配支持 `darwin`/`macos`/`osx`/`apple-darwin`、`arm64`/`aarch64`/
  `x86_64`/`x86_64h`，以及 `universal`/`universal2` 胖二进制。
- `.app` 应用包会自动识别（zip/tar 中只有 bundle 时，例如
  [motty](https://github.com/t8y2/motty)）：各版本保存在数据目录，
  `/Applications/Motty.app` 是指向当前版本的软链；升级/回滚原子切换软链，
  `ghpm remove` 会删除该链接和所有版本。目录可用 `defaults.apps_dir` 或
  `GHPM_APPS_DIR` 调整；`/Applications` 不可写时回退到 `~/Applications`
  并给出 warning。归档同时包含 CLI 和 `.app` 时仍按 CLI 安装，可用
  `install.type: app` 或 `install.bin` 改变选择。
- 只有 `.dmg` 时也能安装：只读挂载镜像，用 `ditto` 把其中的 `.app` 复制到
  版本目录，再创建 `apps_dir` 软链，卸载时自动 detach 镜像。release 同时
  提供 zip/tar.gz 时优先使用归档（更快，不需要挂载）。
- `.pkg` 资产可配 `install.type: pkg`，通过 `sudo installer -pkg ... -target /`
  安装。
- 安装、升级或回滚后会递归清除 `com.apple.quarantine` 扩展属性（原生 xattr
  系统调用），避免 Gatekeeper 阻止运行下载的二进制；`.pkg` 由系统安装器处理。
  清除属性不会为二进制重签名，未做 ad-hoc 签名/公证的 arm64 程序仍可能被
  系统拒绝。
- 自动检查更新可用 launchd，模板见下文。

### 私有仓库与自举

设置 `GITHUB_TOKEN` 后，API 查询和 release 资产下载都会携带鉴权，可以直接
管理私有仓库。也可以让 ghpm 管理自己的 release：

```bash
export GITHUB_TOKEN=ghp_xxx
ghpm add yangjinhe/ghpm --install
ghpm check          # 有新版时会提示
ghpm update --yes   # 自更新到最新 release
```

下载会走 GitHub API 资产端点，重定向到对象存储时自动去掉 Authorization 头。

## 命令一览

| 命令 | 说明 |
|---|---|
| `ghpm add <owner/repo\|url>` | 解析最新 release，生成资产映射并写入配置 |
| `ghpm install [id...]` | 安装缺失的应用；`--all` 安装/升级全部 |
| `ghpm list [--json]` | 已配置与已安装状态 |
| `ghpm check [id...] [--json] [--notify]` | 检查更新；`--notify` 有更新时弹桌面通知 |
| `ghpm update [id...] [--yes]` | 检查并升级；`--all` 不筛选直接升级全部 |
| `ghpm rollback <id> [version]` | 切换到已安装的旧版本 |
| `ghpm pin <id> [version] [--unpin]` | 固定版本/取消固定（固定后跳过更新） |
| `ghpm remove <id...> [--purge]` | 删除文件与软链；`--purge` 同时删配置 |
| `ghpm prune [id...] [--keep N]` | 清理旧版本目录 |
| `ghpm assets <repo>` | 列出 release 资产及匹配打分，便于排查 |
| `ghpm doctor` | 路径、权限、token、限流诊断 |
| `ghpm config [--path]` | 查看配置文件 |
| `ghpm tui` | 交互式终端界面 |
| `ghpm web [--listen addr] [--open]` | 本地浏览器界面（默认 127.0.0.1:8787） |

通用参数：`--config/-c <file>`、`--proxy <url>`、`--no-cache`、`--json`。

凡是接收应用参数的命令（`install`、`check`、`update`、`remove`、`pin`、
`rollback`、`prune`）都接受三种写法：配置中的 app id、`owner/repo` 或
GitHub URL。例如下面三条等价：

```bash
ghpm remove HGhub
ghpm remove Minireel/HGhub
ghpm remove https://github.com/Minireel/HGhub
```

注意：`remove` 默认保留配置项（方便重装），需要连配置一起删时加 `--purge`。

同一仓库可以配置多个应用（使用不同的 `--id`），例如 CLI 与 GUI。自动选择
遇到多个同分候选时，终端下 `ghpm add` 会列出候选并提示选择；非交互环境
会报错并提示用 `--asset` 指定。

安装/更新相关：`--os`、`--arch`（交叉准备配置）、`--tag`（指定版本）、
`--force`（覆盖非 ghpm 管理的同名文件）。

## Web 界面

`ghpm web` 启动一个仅监听本机的 HTTP 服务，用浏览器管理同一份配置：

- 列表、搜索、筛选（可更新/已安装/未安装），多选批量安装/升级；
- 检查更新、添加应用（可选立即安装）、固定、回滚、移除；
- 下载/安装进度通过 Server-Sent Events 实时推送，任务日志可展开；
- 写操作要求 `application/json` 并校验 Host 为回环地址，用于防御
  DNS rebinding；默认只监听 `127.0.0.1:8787`。确需远程访问时用
  `--allow-remote`，并自行加反向代理与认证。

```bash
ghpm web                 # 打开 http://127.0.0.1:8787
ghpm web --open          # 同时调用默认浏览器
ghpm web --listen 127.0.0.1:9000
```

界面资源内嵌在二进制里，不需要 Node.js 或前端构建步骤。

## 原生桌面界面（实验）

`gui/` 提供了一个 MyGo 原生 UI 前端（纯 Go、无 cgo、无 WebView），与
CLI/TUI/Web 共用 `internal/app` 和 SQLite 状态：

```bash
make gui        # 需要 Go 1.27.3+，go.mod 会自动选择工具链
./ghpm-gui
```

功能包括：应用表格与筛选、检查更新、全部更新、添加、安装/升级、固定、
回滚、详情、卸载（可选删除配置）、实时进度面板、原生菜单、桌面通知和
单实例。Linux 上使用 OpenGL/CPU 绘制，不依赖 WebKitGTK。

GUI 使用独立的 release 通道：tag 形如 `v0.5.0-gui`，发布时标记
`make_latest=false`，所以不会占用 CLI 的 `latest`，也不会和 CLI 资产混在
同一个 release 里。安装：

```bash
ghpm add yangjinhe/ghpm --channel tag:v0.5.0-gui --id ghpm-gui --install
```

macOS 发布 universal dmg（安装为 `/Applications/ghpm-gui.app`），Linux 发布
tar.gz 与 deb。频道固定为 GUI tag，`ghpm check` 不会自动发现新的 GUI 版本，
升级时指定新 tag。细节与打包方式见 [`gui/README.md`](gui/README.md)。

## 配置文件

默认位置：Linux 为 `~/.config/ghpm/config.yaml`，macOS 为
`~/Library/Application Support/ghpm/config.yaml`。可用 `GHPM_CONFIG` 或
`-c` 覆盖；设置了 `XDG_CONFIG_HOME` 时优先使用 XDG 路径。

```yaml
version: 1

defaults:
  bin_dir: ~/.local/bin            # 软链目录（两个平台一致）
  data_dir: ~/.local/share/ghpm    # 版本与状态（macOS 默认 ~/Library/Application Support/ghpm）
  apps_dir: ""                     # macOS .app 软链目录，默认 /Applications
  verify: if-available             # required | if-available | skip
  prefer: gnu                      # Linux 上 gnu / musl 偏好
  concurrency: 4                   # 并发下载/检查
  prune_keep: 3                    # 每个应用保留的版本数
  proxy: ""                        # http://127.0.0.1:7890 或 socks5://...
  github:
    token_env: GITHUB_TOKEN        # 建议设置，未认证仅 60 次/小时
    api_base: https://api.github.com
    download_prefix: ""            # 例如 https://ghfast.top 或 https://m.example/{url}
    cache_ttl_min: 30              # check 的元数据缓存；install/add/update 会强制 revalidate

apps:
  # 1) 最精确：按平台写死资产名（支持 {version} {tag} 占位符）
  - id: lazygit
    repo: jesseduffield/lazygit
    match:
      linux/amd64: "lazygit_{version}_Linux_x86_64.tar.gz"
      linux/arm64: "lazygit_{version}_Linux_arm64.tar.gz"
    install:
      type: archive
      bin: lazygit

  # 2) 模板：{arch} = amd64/arm64，{arch_alias} = x86_64/aarch64
  - id: fd
    repo: sharkdp/fd
    match:
      template: "fd-{tag}-{arch_alias}-unknown-linux-gnu.tar.gz"
    install:
      type: archive
      bin: fd

  # 3) 正则兜底：命名不规则时使用
  - id: custom
    repo: owner/custom
    match:
      regex: '^custom-v(?P<version>[0-9.]+)-(?P<arch>x86_64|aarch64)-linux\.tar\.gz$'
    install:
      type: archive
      bin: custom

  # 4) 固定版本 / 只跟预发布
  - id: bat
    repo: sharkdp/bat
    channel: tag:v0.24.0
    match:
      linux/amd64: "bat-v{version}-x86_64-unknown-linux-gnu.tar.gz"
    install:
      type: archive

  # 5) 交给系统包管理器（会调用 sudo，需谨慎）
  - id: bottom-deb
    repo: ClementTsang/bottom
    match:
      linux/amd64: "bottom_{version}-1_amd64.deb"
    install:
      type: deb
      sudo: prompt

  # 6) 要求 checksum 清单带分离签名
  - id: signed-tool
    repo: owner/signed-tool
    match:
      template: "signed-tool-{tag}-{arch_alias}-linux.tar.gz"
    install:
      type: archive
    signature:
      type: minisign            # minisign | gpg
      key: RWQf...              # 公钥文件路径、内联 base64 或整个 .pub 内容
      mode: required            # if-available | required | skip
```

### 签名校验

默认只核对 SHA-256。要防“发布账号被接管后替换资产”，可以要求 checksum
清单带分离签名：

```yaml
signature:
  type: gpg                      # minisign | gpg
  key: ~/keys/maintainer.asc     # GPG 公钥文件；也可填指纹或内联公钥
  mode: required                 # if-available（默认）| required | skip
```

- `minisign`：原生校验，支持默认的 BLAKE2b-512 预哈希（`Ed`）和旧式
  `ED`；`key` 可以是公钥文件、内联 base64 或整个 `.pub` 内容。
- `gpg`：调用系统 `gpg`。`key` 为公钥文件（或内联公钥）时导入临时
  keyring 校验；填指纹/Key ID 时使用当前 keyring，并要求签名者匹配。
- 签名文件按 `SHA256SUMS.sig`、`.minisig`、`.asc`、`.gpg` 等命名在
  release 中自动查找，也可用 `signature.asset` 显式指定。
- 签名存在但校验失败会中止安装；`if-available` 只影响“没有签名或没有
  清单”的情况。`verify: skip` 与签名校验互斥。

### 匹配规则

- `match` 的平台键：`os/arch`，支持 `linux/*`、`*/amd64`、`*/*`、`any`。
  `os`/`arch` 也会做别名归一（`x86_64`→`amd64`、`aarch64`→`arm64`）。
- 值可以是精确文件名或 glob（`*`、`?`、`[]`）；`{version}` 是去掉 `v`
  的版本号，`{tag}` 是原始 tag。
- 没有匹配到当前平台的条目时，会回退到自动打分，而不是直接报错。
- `ghpm assets <repo>` 可以看到每个资产的得分和最终选择（`*`）。

### 安装类型

| type | 行为 |
|---|---|
| `archive` | 解压 tar.gz / tar.xz / tar.zst / zip，按 `bin` 找可执行文件；`strip_components` 可调。归档里只有 `.app` 时自动按应用包安装 |
| `binary` | 裸二进制；软链名默认使用 app 的 `id` |
| `app` | macOS：把 `.app` 安装到版本目录并软链到 `apps_dir`（默认 `/Applications`） |
| `dmg` | macOS：只读挂载磁盘镜像，用 `ditto` 取出其中的 `.app` 后按应用包安装 |
| `appimage` | 放入版本目录并加可执行权限 |
| `deb` / `rpm` | 调用 `apt/dnf/zypper/rpm`，可配 `sudo: never|prompt` |
| `pkg` | macOS：调用 `installer -pkg ... -target /`，可配 `sudo` |
| `script` | 执行安装脚本，默认禁用，需 `allow_scripts: true` |

`install.link` 可覆盖软链名；`install.bin` 指定可执行文件；`install.args`
传给包管理器。

## 目录与状态

```
~/.local/share/ghpm/                # Linux；macOS 为 ~/Library/Application Support/ghpm
├── apps/<id>/<version>/...        # 每个版本独立目录
├── state.db                       # 安装记录（SQLite 单文件）；旧版 state.json 自动迁移后删除
└── cache/ (XDG_CACHE_HOME/ghpm)   # macOS 为 ~/Library/Caches/ghpm
    ├── releases/<hash>.json       # ETag 元数据缓存
    ├── downloads/<id>/<ver>/...   # 已下载资产（重装不再联网）
    ├── checksums/<id>/<ver>/...   # 校验清单
    ├── signatures/<id>/<ver>/...  # 清单的分离签名
    └── packages/                  # deb/rpm/pkg 缓存
~/.local/bin/<name> -> ../share/ghpm/apps/<id>/<ver>/<file>
/Applications/<Name>.app -> ~/Library/Application Support/ghpm/apps/<id>/<ver>/<Name>.app
```

首次运行新版本时，如果数据目录里存在旧的 `state.json`，会自动导入
`state.db`（保留版本、pin、检查缓存等全部字段），成功后删除
`state.json` 与 `state.json.lock`；JSON 损坏时会中止并保留原文件，修复后
下次启动重试。

## 自动检查更新

`ghpm check --notify` 在有更新时弹出桌面通知：Linux 使用 `notify-send`，
macOS 使用 `osascript`。适合放进 cron、systemd user timer 或 launchd。

Linux（systemd）模板：

```bash
mkdir -p ~/.config/systemd/user
cp contrib/systemd/ghpm-check.* ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now ghpm-check.timer
```

macOS（launchd）模板，每天检查一次：

```bash
mkdir -p ~/Library/Logs/ghpm ~/Library/LaunchAgents
sed -e "s|__GHPM_BIN__|$HOME/.local/bin/ghpm|" \
    -e "s|__LOG_DIR__|$HOME/Library/Logs/ghpm|" \
    contrib/launchd/io.github.ghpm.check.plist \
    > ~/Library/LaunchAgents/io.github.ghpm.check.plist
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/io.github.ghpm.check.plist
launchctl kickstart -k gui/$UID/io.github.ghpm.check   # 立即运行一次
```

LaunchAgents 不继承 shell 环境，需要 token 时执行
`launchctl setenv GITHUB_TOKEN ghp_xxx`（对当前登录会话生效）。

Linux 上 token 也可以放在 `~/.config/ghpm/env`（`GITHUB_TOKEN=...`，权限 600），
systemd 服务会读取它。

也可以只用 cron：

```cron
0 9 * * *  /home/you/.local/bin/ghpm check --notify --json >> ~/.cache/ghpm/check.log 2>&1
```

TUI 启动时也会自动检查一次。

## Shell 补全

```bash
ghpm completion bash > ~/.local/share/bash-completion/completions/ghpm
# 或 zsh / fish，见 `ghpm completion --help`
```

## 安全说明

- 默认用户级安装，**不会隐式 sudo**；`deb/rpm/pkg` 需要显式配置且会提示。
- 通过 ghpm 安装的文件会自动清除 macOS quarantine 属性；从浏览器获得的可执行
  文件仍受 Gatekeeper 约束。
- 私有仓库需要设置 `GITHUB_TOKEN`；资产通过 GitHub API 端点携带 token 下载，
  重定向到对象存储时会自动去掉 Authorization 头。
- SHA-256 校验失败会删除下载文件并中止，旧版本和软链保持不变。
- 可选的清单签名校验在哈希校验之前执行；签名不匹配直接中止安装。
- Web 界面默认只绑定回环地址，校验 Host 头，写操作强制 JSON。
- 解包拒绝绝对路径、`..` 穿越和越界符号链接；相对符号链接只有在解析后仍
  位于解包目录内时才保留（macOS `.app` 的 framework 链接需要），任何通过
  符号链接写入后续条目的归档都会被拒绝。
- Token 只从环境变量读取，不写入配置或日志。
- 只允许 HTTPS；镜像前缀只拼接下载地址，不改变资产路径。
- 安装脚本类型默认关闭。

## 已知限制（v0.3）

- 不做依赖解析、源码编译、GitHub Actions artifacts 安装。
- 签名只覆盖 checksum 清单；不验证 cosign/Sigstore 的无密钥签名。
- 不自动配置 shell 补全/桌面文件；软链名默认取 app id，可用 `install.link` 调整。
- macOS：`.app` 以软链方式放入 `/Applications`（或配置的 `apps_dir`），不复制
  整个 bundle；`.dmg` 仅支持其中包含 `.app` 的镜像，镜像内仅有 `.pkg` 时仍
  需手工安装。ghpm 自身未做 Apple 代码签名/公证。
- Windows 的安装路径与包类型未做专门适配（核心逻辑可复用）。
- Web 界面面向本机单人使用，没有内置账号体系。
- 原生桌面界面（`ghpm-gui`）仍是实验性质，通过独立的 `vX-gui` release
  分发；签名与公证流程待接入。

## 持续集成与发布

- `.github/workflows/ci.yml`：每次推送/PR 在 Ubuntu 和 macOS 上分别运行
  gofmt、vet、测试和 race 测试。
- `.github/workflows/release.yml`：推送 `v*` 标签时自动交叉编译
  linux/{amd64,arm64}、darwin/{amd64,arm64}，附带 README、示例配置和
  man page；Linux 额外产出 `.deb`（含手册页），分别上传 sha256 到 Release。
  构建与发布分成两个作业，发布说明直接取 `CHANGELOG.md` 对应版本章节，
  并附上 previous tag 的 compare 链接。

生成 man page：

```bash
make docs          # 输出到 docs/man/
```

本地发布：

```bash
make build VERSION=0.3.0
```

## 开发

```
cmd/ghpm            CLI 入口
internal/cli        cobra 命令与进度输出
internal/tui        bubbletea 界面
internal/web        本地 Web UI（内嵌静态资源 + SSE 进度）
internal/app        编排：add/install/check/update/rollback/remove
internal/config     YAML 配置、路径、仓库解析
internal/gh         GitHub REST/GraphQL 客户端（ETag、限流、批量检查）
internal/resolver   平台别名、资产打分与配置生成
internal/download   断点续传、镜像前缀、SHA-256
internal/archive    安全解包、可执行文件定位
internal/installer  原子安装、软链、版本清理
internal/signature  minisign / GPG 分离签名校验
internal/state      state.db（SQLite）+ 旧 state.json 自动迁移
gui                 MyGo 原生桌面界面（独立 Go 1.27.3 模块）
```

设计原则：平台识别与资产选择是纯函数，便于表驱动测试；网络、文件系统
操作集中在少数包；CLI、TUI 和 Web 只调用 `internal/app`。
