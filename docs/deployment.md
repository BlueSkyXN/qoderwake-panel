# 部署、升级与发布

适用于 Panel 0.12.2。2026-10-06 已公开发布，并完成参考生产 Panel 单独升级，19/19 运行文件一致；生产 daemon/旧网关未重启或首次迁管，网关日志轮转尚未在该旧网关启用。运行时仅需 Python 标准库；Linux 受管启停还要求 `/proc`、bash，以及 Python / 内核的 pidfd 支持。连接快照使用 `ss`。本项目不安装 nginx，也不自动安装或启用 systemd。

## 新部署

1. 先安装官方 QoderWake，并通过官方登录流程建立可用的 daemon HOME；确认 daemon 已启动。Panel 不授予官方账号、身份或权益。
2. 保留完整源码目录结构，尤其是 `panel/` 的 Python 模块、`static/` 和 `config/`，以及 `ops/`。运行数据使用独立目录，不放进源码发布包。
3. 在 `panel/panel.env` 保存实例配置，权限设为 `0600`。下列路径为示例，必须替换为自己的实际路径；该文件不得提交或公开。

```bash
QW_ROOT=/path/to/panel-data
QW_HOME=/path/to/daemon-home
QW_DAEMON_BIN=/path/to/qoderwake-cn
QW_DAEMON_URL=http://127.0.0.1:19830
QW_BIND=127.0.0.1
QW_PORT=19831
```

4. 执行 `bash panel/restart-panel.sh`。它会启动并核对精确进程、监听端口与 `/api/health` 的版本，然后写入 `$QW_ROOT/process-state/panel.json`。
5. 在浏览器登录表单中使用 `$QW_ROOT/admin-token.txt` 或 `viewer-token.txt`。只在部署机本地读取凭据，不放进 URL、日志或 issue。daemon 的 `.auth/token` 必须属于 Panel 运行用户，且没有 group/world 权限；认证失败时停下来核对官方认证与文件权限，不修改官方认证边界。

非回环访问仍受认证、同源和 CSRF 检查，但 HTTP 不提供传输加密。原生 HTTPS 是可选能力，配置方法见 [README](../README.md#远程访问与原生-https)。升级时保留既有绑定和传输方式，不隐式改网络或系统信任。

### TLS 实例的受管健康检查

0.12.1 的 `restart-panel.sh` 根据 `QW_TLS_CERT` / `QW_TLS_KEY` 选择 HTTP 或 HTTPS；两文件必须同时配置。TLS 探测固定连接 `127.0.0.1:$QW_PORT`，不把证书主机名当作新的网络目的地。

- `QW_HEALTH_SERVER_NAME`：证书覆盖的 DNS 名称，用于 SNI 与主机名验证；未设时验证回环 IP。
- `QW_HEALTH_CA_FILE`：可选的可信 CA PEM，仅供该健康探测使用；未设时使用 Python 默认信任库，不修改系统信任。
- 手工调用控制器时使用 `https://127.0.0.1:<port>/api/health`，并按需传 `--health-ca-file` / `--health-server-name`。证书错误不会通过探测，不提供跳过验证参数。

### 显式上行开关与实例绑定

受管 Panel 与 daemon 保留 `QODERWAKE_SESSION_PROJECTION_UPLINK`、`QODERWAKE_REMOTE_EXECUTION_UPLINK` 的显式值；只接受 0/1、false/true、off/on（忽略大小写和首尾空白）。未配置仍沿用官方默认行为，不据此宣称零上行。两个关闭项可能影响官方同步/远程执行上报，须由操作者明确选择。

同一受管实例重启时，state 已记录为关闭的项若缺失或变为开启，会在停止旧进程前拒绝。确需改变该策略时，必须先使用匹配当前 state 的参数显式停止并保留恢复材料，再以新配置启动，不编辑 state 绕过检查。

`status`、`stop`、`start` 都核对既有 state 与本次 HOME、端口、启动路径、命令和 mode/endpoint。更改 HOME、端口或 HTTP/TLS 协议时先按旧参数停止，不能用新地址的健康响应证明旧实例健康。state schema 仍为 1；格式兼容不代表旧非受管进程自动获得 state。

## 升级已受管的 Panel

不要先覆盖当前进程身份记录所引用的脚本：停止时还会检查脚本的 inode/hash，提前替换会使身份核对失败。

1. 准备经过测试的干净候选，并在独立目录完成 Python、JavaScript、shell 语法与发布检查。
2. 安排面板维护窗口，协调外部 CLI/IM 写入。不要把 Panel 的维护门当作 daemon 全局锁。
3. 在当前版本仍完整时，使用当前 `process-control.py stop` 停止 Panel。所传 `--root`、`--name panel`、`--profile panel`、`--launch`、`--script`、`--home`、`--port`、`--mode panel`、`--log`、`--health-url` 必须与部署实例一致；禁止按名称批量杀进程。
4. 备份旧运行文件、`panel.env`、令牌文件及 SQLite 数据库。数据库应在 Panel 停止后复制，或使用 SQLite backup API 创建一致快照。敏感备份只留在部署机私有目录，权限 `0700` / `0600`。
5. 替换面板代码及其全部依赖，保留运行配置、令牌、daemon HOME、Provider 配置和网关规则。
6. 使用新版 `panel/restart-panel.sh` 启动。核对版本、进程身份、端口、管理员和只读权限，以及模型/机器人等只读接口；实际模型调用或资源写入另行验收。
7. 若验收失败，先使用仍匹配的新版本控制器停止新 Panel，再恢复旧代码和必要的数据库快照，最后按旧版本入口启动并核对健康。不要在新进程运行时先覆盖它的脚本。

面板升级不要求重启 daemon 或网关。旧 daemon / 网关未受管时，新面板可以连接官方 API，但其重启、切换和运行态确认会保持受限或未知。

## 首次迁移旧非受管进程

0.12 不会根据进程名、旧 PID 文件或被占用的端口自动接管旧进程。直接调用新版启动器会拒绝未知监听，这是预期保护。

首次迁移应作为单独维护步骤：记录并核对旧进程 PID、start ticks、boot ID、UID、可执行文件、完整 argv 和 socket owner；通过已验证的进程身份与 pidfd 停止唯一目标；确认端口释放，再用新版启动器启动并建立 state。身份不可读、多候选或端口归属不确定时停止操作，不伪造 state、不降级为 `pkill -f`。回滚材料必须包含旧启动环境与入口，而不只有旧源码。

Panel、daemon 和网关分别迁移。只升级 Panel 时，不顺带停止或替换生产 daemon / 网关；网关首次迁移需要独立的规则、启动、业务与精确回滚验收。

## 0.12.2 的兼容事项

- 必须包含新增 `panel/log_io.py` 和 `panel/usage_store.py`；扁平部署也需与主脚本同目录，不只替换主文件。
- `runtime-policy.json` 可选 `sessionProjectionUplink` / `remoteExecutionUplink` 为 true、false 或 null；null/缺失表示继承 Panel 启动环境，不代表关闭。网页保存不会重启；这些文件设置只在 Panel 发起 daemon 启动时转换为环境变量，手工 CLI 启动仍须显式设置相应变量。
- `/api/runtime` 的 `pendingRestart:null` 表示未知，客户端不得将其转成已应用或必须重启。缺少受管身份时页面禁用自动重启，但不伪造 state 或停掉旧进程。
- 新网关日志目录是 `$QW_ROOT/logs/gateway/`，由管理器设为网关用户所有、0700；文件为 0600，每份上限 8 MiB，保留三份历史。父目录必须允许该用户遍历；Panel 读取也须具备权限，否则页面显示日志不可读，不自动放宽权限。旧 `$QW_ROOT/logs/uplink-gw.jsonl` 保留原样，新文件存在后面板优先读取新目录。
- `usage.db` 自动新增游标和重复行计数表，旧事件不重计；首次回填按请求继续，不再因为大于 16 MiB 整份跳过。升级前仍做一致备份，旧版回滚不能使用新游标能力。
- 工具不自动迁管或安装 systemd。参考生产已按独立步骤仅升级 Panel，daemon/旧网关保持不变；隔离回滚通过不等于生产回滚，交付证据见 [验收矩阵](acceptance.md)，修复阶段见 [记录](development-0122.md)。

## 干净快照发布

- 手工挑选已验证且脱敏的源码、文档、示例和测试。不要复制私有 Git 历史，也不要把实验工作目录整体打包。
- 令牌、真实 `settings.json`、`panel.env`、数据库、日志、恢复点、`process-state/`、`gateway-runtime/`、补丁运行数据、`__pycache__` 和 `.DS_Store` 不进入候选。
- 在候选目录运行 README 中的测试与 `bash scripts/check-release.sh`。发布门禁是卫生检查，不是安全认证；仍须审查新增文件和发布 diff。
- 公开仓使用独立提交和版本标签。源码快照可包含零补丁与补丁工具，但不默认应用补丁，也不把本地 fixture 验收写成生产业务完成。

已知功能与验收边界见 [验收矩阵](acceptance.md)。
