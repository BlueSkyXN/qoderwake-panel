# QoderWake Panel

QoderWake 的多人维护工作台：共同编辑 Waker 和 Skill，管理模型、官方操作、会话及运行资源。

**0.13.0-rc.1** 使用 Go 服务与 React 界面，提供 macOS arm64、Linux amd64 独立程序；稳定版仍为 [v0.12.7](https://github.com/BlueSkyXN/qoderwake-panel/releases/tag/v0.12.7)。升级前阅读 [0.13 发布说明](docs/releases/0.13.0-rc.1.md)和[部署与回退](docs/deployment.md)。

本项目是独立社区工具，与 Qoder / QoderWake 官方无隶属关系。项目源码采用 [PolyForm Noncommercial 1.0.0](LICENSE)，商业使用需另行授权；依赖许可随二进制包的 `THIRD_PARTY_NOTICES.txt` 提供。

## 能力

- 本地人员账号、管理员／维护者／观察者角色与 Waker 范围；Skill 正文、版本、差异、回滚及协作冲突处理。
- 明文 BYOK 多模型 JSON 编辑、API Key 保留／更换／删除、原子保存与回读；原生 BYOK 访问状态独立展示。
- Waker 无项目默认、项目模型覆盖、推理强度和上下文窗口；同时展示已有会话实际模型与重启要求。
- 国际版 1.1.13 的 373 项官方 CLI 操作目录，包含参数、权限、实时 help 检查、任务输出、交互输入和文件传递。
- 聊天、会话、任务产物、记忆、用量、插件、IM 通道、自动化、备份、调用者密钥与审计。
- macOS LaunchAgent、Linux systemd 用户服务配置生成，daemon 与网关的受管生命周期、策略、日志与回退。

## 下载与启动

先安装并登录官方 QoderWake，让 daemon 可用。Panel 需要以能够访问该 Wake 数据目录和系统凭据库的用户运行。

从 [Releases](https://github.com/BlueSkyXN/qoderwake-panel/releases) 下载对应平台的 `.tar.gz`、`SHA256SUMS` 和 `RELEASE-MANIFEST.json`，核对压缩包的 SHA-256 后解包。以 macOS 包为例：

```bash
shasum -a 256 qoderwake-panel-v0.13.0-rc.1-darwin-arm64.tar.gz
tar -xzf qoderwake-panel-v0.13.0-rc.1-darwin-arm64.tar.gz
cd qoderwake-panel-v0.13.0-rc.1-darwin-arm64
export QW_ROOT="$HOME/.local/share/qoderwake-panel"
./panel init-admin --root "$QW_ROOT" --username owner --name 管理员
./panel serve --root "$QW_ROOT" --home "$HOME/.qoderwake"
```

比较第一条命令的输出与 `SHA256SUMS` 对应条目。Linux 使用 `linux-amd64` 压缩包和相同启动参数，也可用 `sha256sum` 核对。

浏览器访问 `http://127.0.0.1:19831/`，使用刚创建的人员账号登录。密码通过终端输入，不放进命令参数。自定义实例可传入 `--daemon-bin`、`--daemon-url`、`--bind` 和 `--port`；完整参数通过 `./panel serve --help` 查看。

程序已嵌入前端和 SQLite 驱动，新实例不需要安装 Node、Go 或 Python。已有 schema1 Linux 网关使用保留的 Python 兼容组件；该情况仍需 `python3`。旧 Python Panel 源码保留作兼容回归，0.13 的推荐入口为上述二进制。

多人角色、账号恢复见 [协作说明](docs/collaboration.md)；用户服务、自启、迁移和回退见 [部署说明](docs/deployment.md)。

## 远程访问与 HTTPS

可以通过已有网络入口或 SSH 隧道访问 Panel：

```bash
ssh -L 19831:127.0.0.1:19831 <ssh-host>
```

使用有效证书提供 HTTPS：

```bash
export QW_TLS_CERT=/path/to/fullchain.pem
export QW_TLS_KEY=/path/to/private-key.pem
export QW_PUBLIC_ORIGIN=https://panel.example.com:19831
./panel serve --root "$QW_ROOT" --bind 0.0.0.0
```

未配置 TLS 时连接使用 HTTP。非回环访问应由维护者按网络条件选择 HTTPS 或隧道；已有实例的绑定、证书和系统信任不会随升级修改。

## 已知边界

- 0.13.0-rc.1 是预发布版本，正式实例切换、真实 IM／模型调用和数据回退仍需按目标环境验收。
- 原生 BYOK 接口返回 `MODEL_SETTINGS_LOCAL_ONLY` 时，需在 Wake 所在电脑的官方客户端管理。Panel 不提供原生凭据迁移；明文编辑只写 `qodercli/settings.json`。
- 已保存的模型偏好与已有会话实际模型分别显示。项目覆盖支持添加和更新，当前没有删除覆盖或清空运行参数的入口。
- Panel 不提供 SSO、独立官方租户或完整官方 Console 身份。插件写入默认关闭，高风险能力受官方接口权限约束。
- 网络策略只覆盖已登记的路径；本项目不提供整机零上行或永久锁版保证。具体行为见 [网络控制](docs/uplink-control.md)。

## 权限与安全

- 管理员维护全局配置、人员与运行资源；维护者编辑分配 Waker 的基础配置和 Skill；观察者只读。旧共享令牌模式仅在显式 `--legacy` 启动时保留。
- Cookie 会话最长 7 天、闲置 12 小时过期，退出立即撤销；原生 HTTPS 自动加 `Secure`。
- Cookie 写请求要求同源 Origin 与 CSRF；API 支持 Bearer。拒绝 URL 凭据。
- 登录与消费请求限速落 SQLite；审计记录时间、调用者标识、角色、路径、目标 ID、状态，不写消息正文或凭据。
- 在增强管理创建独立调用者 key，程序使用 `Authorization: Bearer <CALLER_TOKEN>`。只允许 `/api/gw`；已接纳尝试消耗一个额度，失败/超时不退款，避免重试造成免费超额。不是 token 计费或自然人身份认证。
- `QW_ALLOWED_CLIENTS` 可设逗号分隔源 IP/CIDR；`QW_REQUIRE_TLS=1` 拒绝非回环明文 API 访问。不信任 X-Forwarded-For。已有公网部署不会被自动改为此模式。
- Provider 测试拒绝非显式 loopback 的私网、链路本地、混合 DNS 与映射 IPv6 地址；一次解析后固定地址连接，HTTPS 仍校验证书/SNI，不跟随重定向。该保护仅覆盖面板探测，不是 daemon 出网防火墙。
- Provider 事务使用完整原始字节 revision、同目录协作锁、最终替换前复核、原子替换和单份 0600 恢复点；现有多模型 Provider 支持 JSON 编辑。不是针对任意外部写入者的文件系统 CAS，编辑期间须暂停官方 CLI 等不使用该锁的配置写入。主动探测先展示最终目标，再消费 5 分钟一次性计划。
- Waker / Provider 删除先生成 fail-closed 影响预览；required unknown、依赖阻断或扫描不完整时没有普通强制删除。DELETE pending/unknown 均保持目标锁，浏览器只查状态/调和，不盲重发。同一个 Panel 进程的依赖写入与删除互斥，未决删除期间保守暂停新增依赖的写请求；外部 CLI/IM 与多 Panel 进程仍需另行停写协调。
- Maintenance admission 只阻止经 Panel 接纳的新写请求；重启/网关切换还要求现有 lease 排空及连续两次 daemon activity 为空，不冒充 daemon 全局任务锁。
- daemon frontend session 使用官方 CLI 合约（本地私有 token + `client=cli`），不伪装浏览器 Console；GET/HEAD 最多重建一次，写请求不自动重放。插件写默认关闭，因为 CLI session 不等价于官方 Console 的高风险确认。
- 网关和 Panel/daemon 启停都使用版本化 state 与平台进程身份、监听端口归属；Linux 使用 `/proc` 与 pidfd；未知监听、PID 复用、pidfd 不可用或身份不可读时不发送信号。`process-state/`、`gateway-runtime/`、operation journal 和恢复文件禁止进入公开快照。

## 开发与检查

构建需要 Go（版本见 `go.mod`）、Node 24 和 Python 3.11+（检查与打包脚本）。

```bash
bash scripts/build-go.sh
go test -race ./internal/... ./cmd/...
npm --prefix web test
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests
node --test tests/test_*.mjs
bash scripts/check-release.sh --tracked
```

`build/panel-darwin-arm64` 或 `build/panel-linux-amd64` 为当前平台产物。架构、兼容组件和数据格式见 [React/Go 说明](docs/react-go-migration.md)。

## 发布

PR 和主线提交运行两平台 CI。版本标签触发同样的验证与打包，生成两个二进制包、对应源码包、清单和 SHA-256。带后缀的版本标记为预发布；流水线不部署目标机器。

发布来源、快照选择与操作顺序见 [维护约定](docs/maintenance.md)。新标签使用新的附件，已有版本和附件保持原样。完整文档入口见 [文档索引](docs/README.md)。
