# 0.12 P0 本地实现与隔离验收

> 历史记录：2026-10-05 全面复查发现八项未覆盖分支，包括畸形 Group/Provider、停止端口 gate 与实例绑定。下文的保护结论不能扩展到这些反例；更正和未发布修复见 [0.12.1 记录](development-0121.md)。

2026-10-03 开始实施，2026-10-04 完成修复与验收。Panel **0.12.0** 已完成本地实现、fixture 回归和服务器面板单独升级；daemon 验证基线仍为 **Linux CN 1.1.6**。未切换生产旧网关、重启生产 daemon、安装 systemd，未执行真实删除、审批或插件安装。源码发布与服务器部署分别验收，详情见验收矩阵。

## Provider 配置事务与探测

- `settings.json` 以完整原始字节 SHA-256 作为 revision。更新持有同路径进程锁与 sidecar `flock`，变换前、写恢复点前、临时文件 fsync 后紧邻 replace 前核对 revision，父目录也 `fsync`。该锁只约束协作写入者；最终校验与 rename 之间仍不是文件系统 CAS，编辑期间须暂停官方 CLI 等外部配置写入。文件读写上限 4 MiB；`providers=null` 仅在内存规范化，不因读取重写磁盘。
- 保留未知 Provider / model 字段；只有单模型 `openai-compatible` bearer Provider 可由 Panel 编辑。多模型、其他协议、其他认证和畸形结构统一只读。
- 恢复点固定为一份 `provider-settings.previous.json`，保存原始字节、权限 0600；不再生成无限时间戳备份。恢复文件与旧备份都不进入公开候选。
- 探测分 plan / execute。plan 只返回规范化后的最终 scheme/hostname/port/path，不出网；execute 绑定调用者、保存配置 revision 或临时 key 摘要，5 分钟一次性消费。
- Provider key 在 DNS 前拒绝空值、超长值和 CR/LF；探测仍使用 DNS 固定、公网地址校验、TLS/SNI、禁代理、禁重定向和 1 MiB 响应上限。

## 删除预检、幂等与不确定结果

- Waker / Provider 删除先调用 `/api/deletion/preview`，每项检查标明 `clear | blocked | warning | unknown` 与 `complete | partial | unavailable`。
- required unknown、必需数据源不完整或已有 blocker 时 fail closed；warning 需逐项确认。
- Provider 引用按精确 `<provider>/<model>` 集合匹配。Waker 没有 daemon revision 时使用安全字段 fingerprint。Workflow 文本只作有边界的 warning，不用“未命中”证明无动态引用。
- preview 绑定 principal、目标、revision/fingerprint、impact digest、warning 与有效期；execute fresh 重扫后一次性消费。
- `Idempotency-Key` 同键同请求返回已有结果，同键不同请求冲突。DELETE HTTP 202 保持 `pending`；响应丢失记为 `unknown`，两者均返回 202 并保持同目标跨 principal/新键锁定，不自动第二次 DELETE。
- 同一个 Panel 进程内，删除与 caller 授权、模型偏好、会话/消费、通道/配对、自动化启动、Skill/插件等依赖写入使用同一非等待资源锁；忙时返回 409，避免排队后使用旧表单。存在 pending/unknown 删除时，保守暂停这组新增依赖写入，拒绝发生在 caller 扣额前。该锁不覆盖多 Panel 进程或外部 CLI/IM，须另行停写协调。
- Waker/Channel 清单的 null/缺失值不再等同空列表；Waker 详情核对目标 ID，畸形模型偏好、Trigger 目标、Group 成员转 unknown 阻断。
- 浏览器把幂等键保存在 `sessionStorage`，恢复时只查 `/api/deletion/status` 和只读 reconciliation。解除 unresolved lock 需要管理员显式确认目标。

## Maintenance admission

- 状态为 `normal / draining / maintenance / restarting`，带 epoch、owner、reason、时间、TTL 和精确 lease 计数。
- admission gate 位于普通写限频、caller quota 与外部副作用之前；维护期间拒绝不会扣 caller 次数。
- `/api/apply`、`/api/runtime/apply` 和受管 `/api/net/config` 原子进入 drain，等待 Panel 已接纳请求清空，再要求连续两次 daemon activity 完整且全部为 0。
- activity 缺失、类型错误、daemon 不可达或超时都 fail closed。manual maintenance 可由管理员退出；不能用 exit 打断 draining/restarting。TTL 只作用于 manual maintenance。
- 该门只覆盖经 Panel 接纳的新请求，不是 daemon 全局工作锁，外部 CLI/IM/其他管理器仍需单独协调。

## daemon CLI frontend session

- 使用官方 CLI 契约：读取本地私有 `.auth/token`，发送 `Authorization: Bearer` 与 `X-QoderWake-Frontend-Session-Client: cli`；不发送 `Origin`，不伪装 `client=console`。
- token 拒绝 symlink、非普通文件、错误 owner、group/world 权限、过大、过短和 CR/LF。
- session 复用 CookieJar；401 时仅 GET/HEAD 可原子替换 jar 并重建一次，重试会去掉旧 Cookie。POST/PUT/PATCH/DELETE 即使调用者传错参数也不会自动重放。
- bootstrap、daemon_json JSON 响应（含状态页模型/Waker 查询）和错误 body 都有 1 MiB 上限并关闭资源；Artifact 使用独立 32 MiB 下载上限。错误只透传 `code / operationId / pendingId / status / retryable / currentRevision`。
- 401/403/404/409/429 的 HTTP 语义优先；只有 HTTP 202 或 400/422 且明确 `status=pending|running|accepted` 才作为 pending 202。
- 插件 install/remove/toggle 默认拒绝；只有显式 `QW_EXPERIMENTAL_PLUGIN_WRITES=1` 才开放，仍需 Panel 确认。原因是 CLI frontend session 不等价于官方 Console 的高风险确认。

## 网关 state、身份与 journal

- `panel/gateway_runtime.py` 定义严格、版本化的 generation/process/state/journal schema；未知键、错误类型、hash、路径逃逸、root/port 不一致全部拒绝。
- generation 保存 gateway source、policy、runtime 与 config hash；previous 只保存 generation spec，不携带旧 PID/nonce。
- Linux 身份同时核对 PID、start ticks、boot ID、UID、解释器 device/inode、完整 argv、config path/hash、port、root、approved upstream 和 nonce。`exited` 与 `/proc` 不可读的 `unknown` 分开；跨管理器信号必须通过 pidfd，pidfd 不可用时 fail closed。Panel 判 healthy 还必须同时通过 generation 文件 hash、监听 socket inode 归属与严格 HTTP health JSON。
- gateway 子进程环境从空白正向构造，只含运行所需字段，不继承 proxy、Python 注入、Panel token、QODERWAKE secret 或其他父环境。
- upstream 只允许 `openapi.qoder.com.cn` / `openapi.qoder.sh`。响应 rewrite 使用验证后的实际 gateway port；固定 19840 的 hash-pinned QCS 补丁在非默认端口下拒绝 plan。
- `operation.json` 覆盖 prepared/new-started/committing。恢复时只对精确识别的进程发送信号；未知 listener、候选身份不可读或 `/proc` 不完整时保留 journal/generation，要求人工处理。只有可验证 `exited` 才允许启动另一代。
- publish 成功后即视为已提交；若 journal unlink/fsync 失败，保留新进程、state、generation 和可恢复 journal，不进入启动失败回滚。apply/rollback 的 action 语义分离，rollback 目标代际不会被当作 apply 候选误删。
- state/journal 通过逐层 `O_NOFOLLOW` 目录 fd 读取；dangling symlink、权限或 I/O 错误不会被当成“不存在”。GC 仅在 state、journal 和全部可见 `/proc/*/environ` 完整时处理无引用代际：先同目录原子移入 quarantine，重新扫描后才删除；新引用或可见性不完整时原子恢复。管理器每次取得锁后还会先恢复上次崩溃遗留的合法 quarantine；原路径冲突则 fail closed 要求人工处理。
- `preflight` 是“不激活预检”：创建、验证并清理临时代际，不处理 journal、不创建日志、不启动监听、不停止旧进程；因此不是文件系统完全只读。

## Panel / daemon 精确启停

- `ops/process-control.py` 为 Panel 与 daemon 提供统一 state：PID、start ticks、boot ID、UID、executable device/inode、launch/script hash、完整 argv、HOME、port、mode、endpoint、health version 和最小环境。
- signal 前同时核对 state、`/proc` 身份与监听 socket inode，并通过 Linux pidfd 发送信号；pidfd 不可用时不降级到按数字 PID 的 `os.kill`。状态缺失但端口被占、PID 复用、脚本/二进制替换、身份不可读或多方占用时 fail closed。
- readiness 要求精确 PID、目标 port 归该 PID 所有以及 `/api/health` 返回版本；Panel 还固定期待 0.12.0。单纯 TCP connect 不算成功。
- `restart-panel.sh`、`qw-ctl.sh`、gateway/direct/sdkflag 包装和 `refresh-jobtoken.sh` 都委托控制器，不再 `pkill -f` / `pgrep -f`。
- Panel 发起 daemon 重启前先调用 `qw-ctl.sh cn status`，要求 state、进程、端口、mode 和 health 同时一致；启动器 JSON 还需与新 state 的 PID/mode/version 一致。
- 本批不安装或启用 systemd。生产旧进程尚未建立该 state，必须在后续维护窗口做首次迁移和 rollback 演练。

## UI 与文案

- 版本同步到 0.12.0。2026-10-04 修复只读用户 `loadState` 错误请求 admin-only maintenance；管理员仍读取完整维护状态，未扩大 viewer 权限。
- 状态刷新使用独立 single-flight 锁与短持有缓存锁；写入递增失效 epoch，旧刷新不能把失效前结果发布成新鲜缓存。最多重试一次，持续变化返回 503。偏好读取失败显示“未知”，不伪装默认 auto。N+1 CLI、日志全扫、SQLite seen 节流等性能工作仍未完成。
- Provider 页面使用 revision CAS 与具体 probe target 确认；Waker/Provider 删除使用结构化影响对话框；维护 banner 和活动计数已接入。
- 官方外观模式表述为 auto/light/dark；暖白/暖黑明确标为 Panel 增强。
- 自动热部署关闭只停止受管启动时的自动热部署开关，不等于停止 manifest 查询、独立升级器或 root 修改。
- HTTP 风险提示保留；HTTPS 不是本批开发前置条件。既有部署按维护者选择保留公网/HTTP，不自动改为 loopback 或 SSH tunnel，admin/viewer 令牌本批也不轮换。官方 memory Embedding 默认关闭，不等于已实现 Embedding BYOK。

## 2026-10-04 复核修复

- JSON `null` state/journal 直接拒绝，不再当文件缺失；GC 保留相关代际。`/proc` 根缺失或 boot ID 不可读时为 unknown，不当 exited。
- 修正 Linux 真进程测试仍注入空 fake `/proc` 的接线。实测进一步发现退出过渡态导致 stop 误报 unknown；gateway/process-controller 现在 signal 前仍核对完整身份，signal 后等待已绑定 pidfd 的退出事件，再检查端口空闲，不再靠 `/proc` 短暂字段判断退出。
- 发布检查按文件名拒绝旧 `settings.panel-bak-*.json`，不依赖凭据值是否碰巧命中正则。
- Linux 临时目录、临时 HOME 与随机回环端口执行全量回归；真实网关 strict/enforce 切换、rollback、注入启动失败并恢复通过，没有使用生产端口或向官方发送业务请求。不是生产部署。

## 验证证据与完成边界

当前自动测试覆盖 Provider CAS/恢复、删除 fail-closed/幂等/unknown、maintenance race/drain、真实 CookieProcessor 401 重建、gateway fake `/proc` 身份、journal 崩溃窗口、GC、最小环境、端口兼容、Panel/daemon exact identity 和 shell 静态约束。最终全量数字以 [验收矩阵](acceptance.md) 的本轮最终运行结果为准。

当前可以声明：**0.12 P0 实现、隔离测试及服务器面板单独升级完成**。仍不能声明：

- 生产旧网关已迁移；
- systemd 已部署；
- 生产 daemon / 旧网关首次受管迁移或生产切换已通过；
- daemon/worker/child-process 全链路零上行；
- 重启后真实 BYOK 聊天、IM、插件或删除已验收；
- 源码快照发布等同于上述生产业务已验收。
