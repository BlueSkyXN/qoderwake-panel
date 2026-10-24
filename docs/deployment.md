# 部署、迁移与回退

适用于 0.13 的 Go Panel。默认只监听回环地址，以本地人员账号登录；官方 QoderWake 作为已安装的外部依赖。下载、校验和首次启动见 [README](../README.md#下载与启动)。0.12 Python 入口的停止和恢复步骤保存在[历史部署说明](deployment-012.md)。

## 实例配置

| 参数／变量 | 用途 |
| --- | --- |
| `--root` / `QW_ROOT` | Panel 数据目录，包括账号、会话、审计、任务和运行记录 |
| `--home` / `QW_HOME` | 官方 Wake 数据目录；默认当前用户的 `.qoderwake` |
| `--daemon-bin` / `QW_DAEMON_BIN` | 官方 `qoderwake` 可执行文件 |
| `--daemon-url` / `QW_DAEMON_URL` | daemon 地址；未指定时读取实例 `run/daemon.port` |
| `--bind` / `QW_BIND` | Panel 监听地址，默认 `127.0.0.1` |
| `--port` / `QW_PORT` | Panel 端口，默认 `19831` |
| `QW_PUBLIC_ORIGIN` | 浏览器访问的完整 origin，用于同源检查 |
| `QW_TLS_CERT` / `QW_TLS_KEY` | 可选原生 HTTPS 证书与密钥，需要同时设置 |
| `QW_ALLOWED_CLIENTS` / `QW_REQUIRE_TLS` | 可选源地址范围与非回环 TLS 要求 |

一个数据目录同时只运行一个 Panel。官方认证和钥匙串须由运行 Panel 的用户正常访问，认证失败时核对用户、HOME 和系统凭据库；不要复制真实 token 到源码或公开配置。

## 用户服务

`service-config` 只生成配置文件，输出路径须尚不存在。初始化管理员后，检查生成文件里的可执行文件、HOME、端口、日志和环境变量，再由用户加载。

macOS：

```bash
./panel service-config --root "$QW_ROOT" --home "$HOME/.qoderwake"   --label com.qoderwake.panel.local   --output "$HOME/Library/LaunchAgents/com.qoderwake.panel.local.plist"
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.qoderwake.panel.local.plist"
launchctl print "gui/$(id -u)/com.qoderwake.panel.local"
```

Linux：

```bash
mkdir -p "$HOME/.config/systemd/user"
./panel service-config --root "$QW_ROOT" --home "$HOME/.qoderwake"   --output "$HOME/.config/systemd/user/qoderwake-panel.service"
systemctl --user daemon-reload
systemctl --user enable --now qoderwake-panel.service
systemctl --user status qoderwake-panel.service
```

平台服务使用生成时解析出的二进制绝对路径。移动或替换二进制前，先停止对应服务。生成配置时会保留已支持的私有环境变量，文件应保持 `0600` 权限。

## 从旧 Panel 切换

1. 保留旧程序和启动配置。按原启动方式停止旧 Panel；不要先覆盖其进程身份记录所引用的脚本。
2. 在停机状态复制完整 Panel 数据目录，或用 SQLite backup API 建立一致快照。包含 WAL 的库不能只复制主文件。敏感备份保存在部署机私有目录。
3. 使用数据副本和独立端口试运行 Go 版本，核对账号、角色、Waker 范围、审计、Provider 和用量记录。每个数据目录只有一个写入者。
4. 核验通过后再切换正式入口。原有账号模式可继续登录；还使用共享令牌的实例可先显式使用 `--legacy`。执行 `init-admin` 会切换人员账号模式、撤销旧共享会话并停用共享 admin/viewer 登录。
5. 检查 `/api/health` 版本、登录、一次指定资源的保存与回读，以及会话和运行记录。实际外部操作按目标实例另行确认。

Panel 升级不自动替换 daemon、改 Provider 密钥、迁移原生 BYOK 或接管未登记的 App/服务。模型配置与会话生效规则见 [模型管理](model-management.md)。

## 回退

停止新 Panel，保留故障现场副本，恢复旧程序和切换前完整数据快照，再用旧启动方式恢复。恢复快照会舍弃切换后的 Panel 数据变更；已经提交给 daemon、文件系统和外部服务的操作需要分别核对结果。

已有 schema1 Linux 网关继续使用 Python 兼容管理器，升级不会将其自动转换为 Go schema2。daemon 和网关的受管启停、回退见 [平台适配说明](react-go-migration.md#网关与旧部署兼容)。
