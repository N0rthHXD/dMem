# Debian 安装与用户级服务

Debian 包只安装 `dmemd`，不包含 `dmem-mcp`。默认配置安装在
`/usr/share/dmem/config.toml`，其中 `${HOME}` 由 dmemd 按当前用户展开。

安装后启用当前用户的 systemd 服务：

```sh
sudo dpkg -i dmem_<version>_amd64.deb
dmemd --config /usr/share/dmem/config.toml config validate
dmemd --config /usr/share/dmem/config.toml init
systemctl --user daemon-reload
systemctl --user enable --now dmem.service
```

服务把数据写入 `~/.local/share/dmem/`，日志写入 `~/.cache/dmem/dmem.log`，并在
`127.0.0.1:7373` 提供 HTTP API。服务文件位于
`/usr/lib/systemd/user/dmem.service`。

构建 Debian 包：

```sh
make package-deb VERSION=0.1.0
```
