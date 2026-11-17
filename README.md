# ghpm — GitHub Release 软件管理器

`ghpm` 把「GitHub Release 里散装发布的二进制」变成可管理的软件包：

> 配置一个仓库 → 自动识别架构/平台、选中正确的 release 资产 → 下载并校验 →
> 安装到用户目录 → 之后一条命令检查更新、选择升级、随时回滚。

面向 **Linux + 用户级安装**：默认不调用 `sudo`，不污染系统目录，所有文件都在
`~/.local` 下。

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
  限流感知；支持固定版本（pin）。
- **中文网络友好**：支持 `proxy` 配置（HTTP/SOCKS5）和下载镜像前缀
  （如 `https://ghfast.top`），镜像失败自动回退直连。
- **TUI + CLI**：同一套核心逻辑，纯键盘操作。

## 构建

需要 Go 1.21+（本项目用 1.23 开发）。

```bash
cd /home/yangjh/aiproject
make build          # 生成 ./ghpm
make test           # 单元 + 离线集成测试
sudo make install   # 安装到 /usr/local/bin（可选）
```

或者：

```bash
go build -o ghpm ./cmd/ghpm
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
```

> 如果 `~/.local/bin` 不在 `PATH` 里，`ghpm doctor` 会提示；把它加进
> `~/.bashrc` 或 `~/.zshrc`：`export PATH="$HOME/.local/bin:$PATH"`。

## 命令一览

| 命令 | 说明 |
|---|---|
| `ghpm add <owner/repo\|url>` | 解析最新 release，生成资产映射并写入配置 |
| `ghpm install [id...]` | 安装缺失的应用；`--all` 安装/升级全部 |
| `ghpm list [--json]` | 已配置与已安装状态 |
| `ghpm check [id...] [--json]` | 检查更新并缓存结果 |
| `ghpm update [id...] [--yes]` | 检查并升级；`--all` 不筛选直接升级全部 |
| `ghpm rollback <id> [version]` | 切换到已安装的旧版本 |
| `ghpm pin <id> [version] [--unpin]` | 固定版本/取消固定（固定后跳过更新） |
| `ghpm remove <id...> [--purge]` | 删除文件与软链；`--purge` 同时删配置 |
| `ghpm prune [id...] [--keep N]` | 清理旧版本目录 |
| `ghpm assets <repo>` | 列出 release 资产及匹配打分，便于排查 |
| `ghpm doctor` | 路径、权限、token、限流诊断 |
| `ghpm config [--path]` | 查看配置文件 |
| `ghpm tui` | 交互式终端界面 |

通用参数：`--config/-c <file>`、`--proxy <url>`、`--no-cache`、`--json`。

安装/更新相关：`--os`、`--arch`（交叉准备配置）、`--tag`（指定版本）、
`--force`（覆盖非 ghpm 管理的同名文件）。

## 配置文件

默认位置 `~/.config/ghpm/config.yaml`，可用 `GHPM_CONFIG` 或 `-c` 覆盖。

```yaml
version: 1

defaults:
  bin_dir: ~/.local/bin            # 软链目录
  data_dir: ~/.local/share/ghpm    # 版本与状态
  verify: if-available             # required | if-available | skip
  prefer: gnu                      # Linux 上 gnu / musl 偏好
  concurrency: 4                   # 并发下载/检查
  prune_keep: 3                    # 每个应用保留的版本数
  proxy: ""                        # http://127.0.0.1:7890 或 socks5://...
  github:
    token_env: GITHUB_TOKEN        # 建议设置，未认证仅 60 次/小时
    api_base: https://api.github.com
    download_prefix: ""            # 例如 https://ghfast.top 或 https://m.example/{url}
    cache_ttl_min: 30              # release 元数据缓存分钟数

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
```

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
| `archive` | 解压 tar.gz / tar.xz / tar.zst / zip，按 `bin` 找可执行文件；`strip_components` 可调 |
| `binary` | 裸二进制；软链名默认使用 app 的 `id` |
| `appimage` | 放入版本目录并加可执行权限 |
| `deb` / `rpm` | 调用 `apt/dnf/zypper/rpm`，可配 `sudo: never|prompt` |
| `script` | 执行安装脚本，默认禁用，需 `allow_scripts: true` |

`install.link` 可覆盖软链名；`install.bin` 指定可执行文件；`install.args`
传给包管理器。

## 目录与状态

```
~/.local/share/ghpm/
├── apps/<id>/<version>/...        # 每个版本独立目录
├── state.json                     # 安装记录（原子写 + 文件锁）
└── cache/ (XDG_CACHE_HOME/ghpm)
    ├── releases/<hash>.json       # ETag 元数据缓存
    ├── downloads/<id>/<ver>/...   # 已下载资产（重装不再联网）
    ├── checksums/<id>/<ver>/...   # 校验清单
    └── packages/                  # deb/rpm 缓存
~/.local/bin/<name> -> ../share/ghpm/apps/<id>/<ver>/<file>
```

## 自动检查更新

`ghpm check` 适合放进 cron 或 systemd user timer，例如每天 9 点：

```cron
0 9 * * *  GITHUB_TOKEN=xxx /home/you/.local/bin/ghpm check --json >> ~/.cache/ghpm/check.log
```

配合通知脚本读取 `--json` 输出即可。TUI 启动时也会自动检查一次。

## 安全说明

- 默认用户级安装，**不会隐式 sudo**；`deb/rpm` 需要显式配置且会提示。
- SHA-256 校验失败会删除下载文件并中止，旧版本和软链保持不变。
- 解包拒绝绝对路径、`..` 穿越和符号链接条目。
- Token 只从环境变量读取，不写入配置或日志。
- 只允许 HTTPS；镜像前缀只拼接下载地址，不改变资产路径。
- 安装脚本类型默认关闭。

## 已知限制（v0.1）

- 主要面向公开仓库；私有仓库的资产下载鉴权尚未实现（API 查询支持 token）。
- 不做依赖解析、源码编译、GitHub Actions artifacts 安装。
- 不自动配置 shell 补全/桌面文件；软链名默认取 app id，可用 `install.link` 调整。
- macOS/Windows 的安装路径与包类型未做专门适配（核心逻辑可复用）。

## 开发

```
cmd/ghpm            CLI 入口
internal/cli        cobra 命令与进度输出
internal/tui        bubbletea 界面
internal/app        编排：add/install/check/update/rollback/remove
internal/config     YAML 配置、路径、仓库解析
internal/gh         GitHub REST 客户端（ETag、限流）
internal/resolver   平台别名、资产打分与配置生成
internal/download   断点续传、镜像前缀、SHA-256
internal/archive    安全解包、可执行文件定位
internal/installer  原子安装、软链、版本清理
internal/state      state.json + flock
```

设计原则：平台识别与资产选择是纯函数，便于表驱动测试；网络、文件系统
操作集中在少数包；CLI 与 TUI 只调用 `internal/app`。
