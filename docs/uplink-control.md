# 网络控制与更新限制：双模式增强

> 修订 2026-10-03。以可验证的实现为准，替代此前把设计目标写成已实现事实的描述。

## 定位

零补丁能力（官方 API、配置、启动参数、独立网关）与补丁能力在同一项目里并存，可独立或组合使用。公私仓只区分发布材料是否已标准化脱敏，不区分功能能力。

“只下不上”在本项目指减少向官方上传任务内容、日志、遥测等数据，不是网络字节严格单向。下载和身份续期也需要向外发请求；GET 不天然安全，POST 也不天然是多余上报。

## 已实现与未达成

| 项目 | 已知状态 |
|---|---|
| 观测网关 | 能记录经过它的请求；不等于覆盖所有进程、所有路径 |
| tracking 过滤 | 已实现该路径的本地过滤，不能扩展宣称所有遥测均零出网 |
| 其他机器注册、QCS、认证流量 | 仍可能发送，不能盲目整组吞掉 |
| lite 防火墙 | 存在实验脚本与现场规则；是主机 OUTPUT 规则，有超出 daemon 的影响范围 |
| full 封网 | 现场测试出现模型列表与聊天回归失败，未通过验收，UI 不提供启用按钮 |
| QCS 补丁 | 0.8 已接入完整哈希注册、确认计划与备份还原；仅一个审核模块/版本，未修改现场 daemon |
| 更新控制 | 0.8 增加官方自动热部署下次启动开关，另保留目录权限观测；不阻止 root 和独立升级器 |
| strict 网关 | 0.9 增加离线候选与人工 approvals 生成；默认拒绝、精确白名单和日志打码通过回归；未替换现场网关、不覆盖直连 |
| 免登录业务 | 未达成。不保证官方登录过期后 BYOK 仍能运行 |
| Embedding BYOK | 本版本未实现 |

## 对既往结论的更正

1. 字符串检索看到 `/api/...` 不能证明它是对外上传；有些是本地 daemon 路由。
2. 单次 `ss` 无外连不证明没有历史外连、worker 外连或重试；SNI 抓到官方域也不能证明所有目的地都已归属，更不证明“第三方泄露为零”。
3. 仅凭某个时段防火墙阻断，不足以归因到 `/api/session-start/`；此前直接称为派发直连源头的结论证据不足。
4. 变量组合奏效不等于“缺任一变量都会忽略”；需要逐变量对照试验才能证明必要性。
5. 运行时拼接 URL 不代表技术上无法补丁；更深入的代码修改仍可能，但未完成定位、安全审查与回归。
6. 更新目录 chmod 只是一层权限限制，尤其 daemon 以 root 运行时，不是完整更新冻结机制。
7. 网关阻断与源头不发送并不等效；源头禁用上报功能尚未交付，未获用户同意不应称为“已按共识放弃”。

## 运行与回滚约束

- 更换网关规则、重启、修改防火墙必须明确确认，先保留回滚材料。
- 面板重启 daemon 时保留当前网关启动方式，拒绝悄悄退回直连模式。
- 当前 UI 仅开放观测/执行、已验证 tracking 规则与 lite/撤销；不承诺恢复官方 token 失效后的功能。
- 既有防火墙脚本并非完整的按进程网络隔离，不应当作多租户生产策略直接复用。
- QCS 补丁只处理特定字面量。0.8 改用完整输入/输出哈希验证及一次性确认、原子替换；旧备份也必须哈希吻合。该管理能力不代表全部补丁模块或严格出网验收完成。
- 上述未达成项保留在验收矩阵，不以“低性价比”替用户取消目标。

## 0.11 网关运维保护

`uplink-gw-launch.sh preflight|apply|rollback|status|stop` 委托 `gateway-manager.py`；运行用户必须已存在，不自动创建或退回 root。`preflight` 是不激活预检：创建并清理临时代际，但不创建日志、不启动监听、不停止旧进程；`apply` 只切换已核对身份的受管进程，失败恢复上一代。旧非受管监听存在时拒绝自动接管，先完成专门的首次迁移与回退验收。

默认策略路径统一为 `$QW_ROOT/config/uplink-gw.json`，代际运行目录为 `$QW_ROOT/gateway-runtime/`。配置与进程记录均不进公开快照。当前生产只完成新版本/实际用户预检和配置权限修复，旧网关没有重启；页面显示“未核实”并禁用应用规则，不将文件里的 enforce 当作运行事实。

自动热部署与官方 memory Embedding 已在本轮空闲重启后核对为实际关闭；其余升级路径与全进程网络隔离仍未闭环。

## 0.12 网关与进程身份

0.12 增加 `panel/gateway_runtime.py` 和版本化 strict state。generation spec 与 process identity 分开；previous 只保存 generation，不携带旧 PID/nonce。信号前核对 PID、start ticks、boot ID、UID、解释器 inode/device、完整 argv、config path/hash、port、root、approved upstream 和 nonce，并通过 pidfd 发送信号；不可用时 fail closed。Panel 页面还要求 generation 文件 hash、监听 socket inode 归 state PID 所有与严格 HTTP health JSON 同时匹配，不能只信端口或健康响应。

网关子进程环境从空白正向构造，不继承 proxy、Python 注入、Panel token 或父进程其他 secret。upstream 只允许两个内建官方域；响应 URL rewrite 使用验证后的实际端口。QCS hash-pinned 补丁仍固定 19840，所以非默认 gateway port 下明确拒绝补丁 plan，不做静默错配。

`operation.json` 记录 prepared/new-started/committing。恢复只停止精确识别的候选；只有可验证 `exited` 才启动另一代。未知 listener、候选身份不可读或 `/proc` 可见性不完整时保留 journal 与 generation，要求人工处理。publish 成功后的 journal 清理故障不会回滚已提交代际。state/journal symlink 或读取异常会阻断 GC；GC 先将候选原子移入同目录 quarantine，再重新扫描 current、previous、journal 与 `/proc` 引用，新引用或可见性不完整时原子恢复；管理器下次取得锁时先恢复崩溃遗留 quarantine，原路径冲突则要求人工处理。0.12.0 当时未包含日志轮转。0.12.2 已发布专用日志目录和 8 MiB / 三份历史轮转，面板总尾读不超过 1 MiB；生产仅升级 Panel，旧网关尚未升级，其日志仍不受新版轮转约束，旧文件不会自动删除。

Panel 与 daemon 启停统一委托 `ops/process-control.py`。state 绑定 PID/start ticks/boot ID/executable/argv/HOME/mode/port/socket owner/health version；`restart-panel.sh`、`qw-ctl.sh` 与 direct/gateway/sdkflag 包装不再按进程名批量杀进程。旧生产进程没有该 state，必须在后续维护窗口完成首次迁移、健康核对和 rollback 演练；本批没有执行。

## 后续验收要求

先用 `ops/gateway-policy.py candidates <redacted-log> <candidates.json>` 汇总已显式允许审计的静态路径，再由维护者编写 approvals 文件并用 `build` 生成 strict 配置；工具不激活策略。对每条拟拦截路径记录调用源、请求语义、敏感字段类别、规则动作和实际转发量。日志不应包含凭据/消息正文/动态账号 ID。覆盖冷启动、模型目录、两轮聊天、资源下载、登录续期与 IM（配置后）后，才能扩大拦截范围。更新控制必须验证升级器、热部署与 root 场景，不能只读 mode bit 宣告完成。
