# 安装

dMem 以用户级方式安装，不包含 systemd 单元或系统包，守护进程由用户自行启动。

## 一键安装（预编译发布包）

从 GitHub 下载与当前架构（`amd64`/`arm64`）匹配的预编译发布包并安装，无需 Go 或
C 编译器：

```sh
curl -fsSL https://raw.githubusercontent.com/N0rthHXD/dMem/main/install.sh | sh
```

脚本把 `dmemd` 安装到 `~/.local/bin/dmemd`，把默认配置安装到
`~/.config/dmem/config.toml`（dmemd 的默认配置读取路径，其中 `${HOME}` 由 dmemd
按当前用户展开），校验归档 SHA-256，执行 `config validate` 与 `init`，并在
`~/.local/bin` 不在 `PATH` 时给出提示。安装并后台启动守护进程：

```sh
curl -fsSL https://raw.githubusercontent.com/N0rthHXD/dMem/main/install.sh | sh -s -- --daemon
```

出于安全考虑，建议先下载审阅再执行：

```sh
curl -fsSL https://raw.githubusercontent.com/N0rthHXD/dMem/main/install.sh -o install.sh
less install.sh
sh install.sh
```

发布包由 `.github/workflows/release.yml` 在推送 `v*` 标签时构建，产出
`dmem_linux_amd64.tar.gz`、`dmem_linux_arm64.tar.gz` 及各自 `.sha256`；手动触发
（`workflow_dispatch`）时可指定版本标签。指定版本安装：

```sh
DMEM_VERSION=0.1.0 sh install.sh
```

## 源码检出安装

在源码检出目录中运行同一个脚本时改为本地构建（需要 Go 1.24 或更新版本、C 编译器
与 `make`）：

```sh
bash install.sh
bash install.sh --daemon
```

安装默认把数据写入 `~/.local/share/dmem/`，并在 `127.0.0.1:7373` 提供 HTTP API。
守护进程日志在 `--daemon` 模式下写入 `${XDG_CACHE_HOME:-$HOME/.cache}/dmem/dmem.log`。

### 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `BINDIR` | `$HOME/.local/bin` | 二进制安装目录 |
| `CONFIGDIR` | `$HOME/.config/dmem` | 配置安装目录 |
| `FORCE=1` | 空 | 覆盖已存在的 `config.toml` |
| `DMEM_VERSION` | `latest` | 要下载的发布标签，或 `latest` |
| `DMEM_PREBUILT=1` | 空 | 即使位于检出目录也强制下载预编译包 |
| `VERSION` | `git describe --tags --always --dirty`，回退 `0.1.0` | 源码构建时写入二进制的版本 |

若目标 `config.toml` 已存在，安装会保留它以避免覆盖本地修改；需要覆盖时使用
`FORCE=1`。

## 底层原语

`make install` 只做构建与拷贝，不做校验或初始化；`install.sh` 的源码路径复用它。

```sh
make install VERSION=0.1.0
```

随后手动完成初始化与启动：

```sh
export PATH="$HOME/.local/bin:$PATH"
dmemd config validate
dmemd init
dmemd daemon
```
