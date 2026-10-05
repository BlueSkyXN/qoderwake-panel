# QoderWake Panel

自管 QoderWake 的增强面板与运维工具。当前源码、公开 Release 与参考生产 Panel 均为 **0.12.1**（2026-10-05），修复全面复查确认的八项安全与兼容性问题，见 [修复记录](docs/development-0121.md)。服务器只升级 Panel，17/17 运行文件核验通过；daemon 基线仍为 Linux CN **1.1.6**，daemon 和旧网关未重启或迁管。源码发布、部署与真实业务验收分别记录在 [验收矩阵](docs/acceptance.md)。

**它不是官方控制台的完整替代，也未实现整机零上行、受保护业务免登录或完全锁版。官方 memory Embedding 默认关闭，通用 Embedding BYOK 暂不作为主线。** 功能与证据边界见 [验收记录](docs/acceptance.md)。

> **非官方声明**：本项目为独立的社区工具，与 Qoder / QoderWake 官方无隶属或合作关系；相关名称与商标归其所有者，此处仅作指代。
>
> **许可**：[PolyForm Noncommercial 1.0.0](LICENSE) —— 允许学习、个人自用、修改与分享等非商业用途；商业使用需事先取得版权所有者授权。

## 功能

- 总览、基础聊天、单模型 OpenAI-compatible Provider 编辑、Waker 默认模型与基础编辑。
- 按模型和日期汇总回合日志，增量持久化；不是 token 计量或计费看板。
- 备份、调用审计、daemon 连接快照、网关规则管理与 QCS 补丁标记检查。
- 官方插件市场搜索、分页和详情查询；插件 install/remove/toggle 默认关闭，因为 CLI frontend session 不等价于官方 Console 高风险确认。只有显式实验开关才开放写入口，未自动安装第三方插件。
- 独立调用者密钥：逐 Waker 范围、到期、撤销、持久化请求次数额度；不授予管理权限或历史读取。
- QCS 补丁完整哈希注册、一次性确认计划、与 daemon 启停共用锁、最终替换前停止进程复核、备份与哈希还原；网页/CLI 都显式绑定网关端口，未知版本或端口不兼容拒绝。
- 官方 IM 已有通道列表、CN 版飞书配置保密编辑、删除与启停管理；平台授权和实际消息仍需现场验证。
- 受管启动默认关闭自动热部署与官方 memory Embedding；可分别显式打开。停止自动热部署不等于停止 manifest 查询、独立升级器或 root 修改；保存与重启分开，当前进程状态单独观测。
- 系统/工作负载状态、高级会话筛选、已读和删除、会话 Artifact 清单及受限下载。
- Skill 从机器人已有技能下拉选择；无正文、内置固定技能和缺版本基线时只读。正文保存保留原始空白，基线冲突拒绝覆盖；版本列表/diff/回滚已接入。
- 网关共用严格配置校验；按实际运行用户预检，不可变代际快照、身份健康检查和失败恢复。旧非受管网关禁止自动接管，现场尚未完成首次迁移。
- 自动化列表、运行历史、启停、确认后执行/删除；IM pending pairing 采用命令和自定义模型均关闭的最小权限批准。
- 官方当前外观模式 auto / light / dark；另提供暖白 / 暖黑两种 Panel 增强主题。响应式布局、双栏登录页、自绘能力卡片。

## 维护方法

私有仓用于实验与研究；公开仓由维护者**手工挑选有用内容、脱敏、标准化后发布**。不自动同步，不搬私有 Git 历史。零补丁与补丁两类能力可以并存；不是两个公私功能版本。

- 零补丁侧：官方 API / CLI / 配置、可选控制面过滤网关。
- 补丁侧：QCS 端点模块通过同一安全管理器提供网页/CLI 入口。仅注册 1.1.6 CN Linux x64 的完整输入和输出哈希；不默认应用，也不等于全部隐私模块已移植。
- 任何升级均需重新验证接口、主题、启动环境和补丁，不能宣称“升级免疫”。

## 启动

Python 3.10+，运行时仅标准库。Linux 受管启停依赖 `/proc` 与 bash；连接快照另用 `ss`。不要求安装 nginx，也不自动安装或启用 systemd。

```bash
export QW_ROOT="$HOME/qoderwake-panel-data"
export QW_HOME="/path/to/daemon-home"
export QW_DAEMON_BIN="/path/to/qoderwake-cn"
export QW_BIND=127.0.0.1
export QW_PORT=19831
python3 panel/qoderwake-panel.py
```

完整保留 `panel/` 和 `ops/` 目录（包括 `panel_security.py`、`static/` 及 `ops/process-control.py`）。面板已不再是单文件分发；daemon 运行状态观测也复用精确进程控制器。

浏览器访问 `http://127.0.0.1:19831/`。访问令牌在 `$QW_ROOT/admin-token.txt` 与 `$QW_ROOT/viewer-token.txt`，权限为 600，请仅在本机终端读取，不粘贴到日志或 issue。

首次登录使用表单；会话是独立随机 ID，不将长期令牌放 Cookie。旧 URL token 不再被 API 接受；已有旧 Cookie 只可经同源迁移接口换取新会话并清除旧 Cookie，不能直接调用业务 API。

### 远程访问与原生 HTTPS

需要回环远程访问时，可使用 SSH 隧道；这是可选部署方式，不改变 daemon 的认证，也不自动调整已有公网绑定：

```bash
ssh -L 19831:127.0.0.1:19831 <ssh-host>
```

已有合法证书时，可直接由面板提供 HTTPS：

```bash
export QW_BIND=0.0.0.0
export QW_TLS_CERT=/path/to/fullchain.pem
export QW_TLS_KEY=/path/to/private-key.pem
export QW_PUBLIC_ORIGIN=https://panel.example.com:19831
python3 panel/qoderwake-panel.py
```

使用 `panel/restart-panel.sh` 受管启动 TLS 实例时，健康探测固定连接本机回环，但可设置 `QW_HEALTH_SERVER_NAME=panel.example.com` 用于证书名称校验和 SNI；私有 CA 另设 `QW_HEALTH_CA_FILE=/path/to/ca.pem`，公有可信证书可不设 CA 文件。不得关闭证书验证。详见 [部署文档](docs/deployment.md)。

未配置证书时仍为 HTTP，Cookie、CSRF、源 IP 限制均**不能替代传输加密**。当前部署按维护者选择继续使用 HTTP；HTTPS 不列为本阶段开发待办，界面保留事实提示。本项目不会擅自安装服务或改系统信任链。

### API

```bash
curl -X POST http://127.0.0.1:19831/api/gw \
  -H 'Authorization: Bearer <ADMIN_TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{"wakerId":"<WAKER_ID>","message":"你好"}'
```

调用可能消耗额度。不能把“SDK 收到了请求”当成模型成功完成。IM 原生由官方支持；实际 IM + BYOK 未配置实测。

## 权限与安全

- admin：管理、会话正文、配置、审计；viewer：状态、模型名称、机器人列表、用量、插件摘要。服务端强制权限，不只隐藏按钮。
- Cookie 会话最长 7 天、闲置 12 小时过期，退出立即撤销；原生 HTTPS 自动加 `Secure`。
- Cookie 写请求要求同源 Origin 与 CSRF；API 支持 Bearer。拒绝 URL 凭据。
- 登录与消费请求限速落 SQLite；审计记录时间、调用者标识、角色、路径、目标 ID、状态，不写消息正文或凭据。
- 在增强管理创建独立调用者 key，程序使用 `Authorization: Bearer <CALLER_TOKEN>`。只允许 `/api/gw`；已接纳尝试消耗一个额度，失败/超时不退款，避免重试造成免费超额。不是 token 计费或自然人身份认证。
- `QW_ALLOWED_CLIENTS` 可设逗号分隔源 IP/CIDR；`QW_REQUIRE_TLS=1` 拒绝非回环明文 API 访问。不信任 X-Forwarded-For。已有公网部署不会被自动改为此模式。
- Provider 测试拒绝非显式 loopback 的私网、链路本地、混合 DNS 与映射 IPv6 地址；一次解析后固定地址连接，HTTPS 仍校验证书/SNI，不跟随重定向。该保护仅覆盖面板探测，不是 daemon 出网防火墙。
- Provider 事务使用完整原始字节 revision、同目录协作锁、最终替换前复核、原子替换和单份 0600 恢复点；复杂 Provider 只读。不是针对任意外部写入者的文件系统 CAS，编辑期间须暂停官方 CLI 等不使用该锁的配置写入。主动探测先展示最终目标，再消费 5 分钟一次性计划。
- Waker / Provider 删除先生成 fail-closed 影响预览；required unknown、依赖阻断或扫描不完整时没有普通强制删除。DELETE pending/unknown 均保持目标锁，浏览器只查状态/调和，不盲重发。同一个 Panel 进程的依赖写入与删除互斥，未决删除期间保守暂停新增依赖的写请求；外部 CLI/IM 与多 Panel 进程仍需另行停写协调。
- Maintenance admission 只阻止经 Panel 接纳的新写请求；重启/网关切换还要求现有 lease 排空及连续两次 daemon activity 为空，不冒充 daemon 全局任务锁。
- daemon frontend session 使用官方 CLI 合约（本地私有 token + `client=cli`），不伪装浏览器 Console；GET/HEAD 最多重建一次，写请求不自动重放。插件写默认关闭，因为 CLI session 不等价于官方 Console 的高风险确认。
- 网关和 Panel/daemon 启停都使用版本化 state 与 Linux `/proc` 精确身份、监听 socket ownership 和 pidfd；未知监听、PID 复用、pidfd 不可用或身份不可读时不发送信号。`process-state/`、`gateway-runtime/`、operation journal 和恢复文件禁止进入公开快照。

## 测试与发布

```bash
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests -v
node --check panel/static/panel.js
node --check panel/static/management.js
node --check panel/static/advanced.js
node --test tests/test_*.mjs
bash scripts/check-release.sh
```

Node 仅用于开发期 JavaScript 语法和回归测试。发布检查只检查候选目录，不拷贝、不 commit、不 push。测试缓存、实际配置、数据库、令牌、备份不得进公开快照；检查通过不等于安全认证。

[公开仓](https://github.com/BlueSkyXN/qoderwake-panel) 与 [v0.12.1 Release](https://github.com/BlueSkyXN/qoderwake-panel/releases/tag/v0.12.1) 已发布，两个源码附件重新下载验证为 66/66 文件与标签提交一致。0.12.0 与 0.12.1 标签和附件保持原样；后续 `main` 可补充交付文档而不移动标签。基线、两仓职责和 PR 规则见 [维护约定](docs/maintenance.md)。

详细设计：[网络控制](docs/uplink-control.md) · [验收状态](docs/acceptance.md) · [部署与升级](docs/deployment.md) · [维护与 PR](docs/maintenance.md)。
