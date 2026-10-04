# 0.8.0 增强能力与仍未闭环的目标

本版不是“所有剩余目标完成”。代码实现、合成回归、服务器读取验证和真实写操作分别记录。

## 独立调用者

管理员在增强管理选择现有 Waker，设置名称、1–365 天有效期、1–100000 次请求上限。只展示一次随机 key；SQLite 仅存 key 哈希。

调用者只能 Bearer 调用 `POST /api/gw`（以及自身身份信息），不能登录管理面板、读历史、创建会话或管理配置。服务端重新检查数据库中的 Waker 范围、过期和撤销状态，以 SQLite 事务扣减次数。统计接纳尝试而不是成功回复数；超时不能证明模型没消费，故不退额度。已有执行请求不因撤销而中断。

管理员/viewer 仍是共享管理角色。本版没有账单金额硬限额、token 计量或每个自然人的账户系统。

## 补丁事务

内建模块 `qcs-endpoint`，注册 CN Linux x64 1.1.6 官方二进制及确定性等长替换后的 SHA-256。默认读取 `panel/config/patch-registry.json`，本机 `QW_ROOT/patch-registry.json` 或显式 `QW_PATCH_REGISTRY` 可覆盖。

网页先生成 5 分钟有效、绑定当前管理员身份的一次性计划，再确认执行。执行需 Linux `/proc` 可见、目标二进制未被进程运行、当前哈希仍匹配、唯一锚点和输出哈希匹配。保存 600 原文件备份后原子替换；还原校验完整备份哈希。历史 `.bak-p1-*` 只有完整哈希匹配才能作为还原源。

CLI 与网页使用同一管理器，不保留旧工具“只看锚点就写入”的旁路：

```bash
python3 ops/patch-qcs-endpoint.py status /path/to/qoderwake-cn
# 停止目标进程后，使用上一步读到的完整当前哈希明确确认。
python3 ops/patch-qcs-endpoint.py apply /path/to/qoderwake-cn --confirm-sha256 <CURRENT_SHA256>
```

本模块需要已配置本机网关及 `qwgw.local.test` 回环解析；不改 hosts、不停进程、不更改系统信任。只匹配完整文件哈希，版本名来自审核注册表；不通过运行未知文件来信任其自报版本。不阻止拥有写权限的本机管理员替换文件，也未完成所有隐私补丁模块。

## 网关默认拒绝（仅本地回归，未部署替换现有网关）

`ops/uplink-gw.py` 新增 `mode: strict`。默认规则为空，所有请求拒绝；规则只能精确 method/path，并明确允许的 query 键、转发头和最大请求体。未匹配返回 403，不伪装 204 成功。配置损坏即启动失败，不退回 observe。禁用自动重定向和环境代理，限制请求体、拒绝歧义分帧，日志打码路径并记录是否尝试转发。

示例只是语法，不是已验证的官方必需路径白名单：

```json
{"mode":"strict","token_policy":"balanced","rules":[{"method":"GET","path":"/catalog","query":["page"],"headers":["accept"],"max_body":0}]}
```

已自动化验证未知请求不会调用上游。此保证仅限经过此网关的请求；不封住 daemon/worker 直连。允许的请求仍会发送 URL、头、TCP/TLS 信息。没有把它宣传成整机“零上行”，未切换现场 full 防火墙，未假装已完成官方授权下的离线执行。

## 官方市场与 IM

插件市场接入 `/api/plugin-market` 的搜索/分页、详情、安装目标查询；写操作调用官方 installations 契约，含 expectedVersion 校验和二次确认。服务器真实读到 49 项目录、首页 20 项、一个插件详情及安装目标。自动化使用模拟 daemon 检查安装/卸载/启停参数；没有为测试安装第三方代码，不能标记真实安装验收通过。

IM 页面显示已配置通道并调用官方 start/stop/restart。返回字段使用白名单，不回传平台秘密。服务器当前无通道，未验证 IM + BYOK；首次平台授权和配对保留官方流程。

## 自动更新与 Embedding

从官方 Linux 实现核实：`QODERWAKE_HOT_DEPLOY=0` 或 `false` 阻止自动热部署轮询，默认值为开启。面板可保存下次启动策略并由面板启动器传入环境；另可设置 `QODER_MEMORY_DISABLE_EMBEDDING=1`。保存不自动重启，页面分别显示期望配置和 `/proc` 实际环境。

现场发现自动热部署仍为开，未擅自停 daemon；“目录无写位”不是完整更新冻结。

官方 `QODER_EMBEDDING_ENDPOINT`、模型和维度变量仍使用 Qoder 身份、Cosy 签名/加密及官方 embedding 路径，不能直接接第三方 Bearer `/v1/embeddings`。通用 Embedding BYOK 仍需单独客户端适配及索引迁移验证。

官方未登录允许 local-only 启动，但受保护业务返回 OWNER_AUTH_REQUIRED；本版不新增伪造 owner/权益或改信任链。完整免登录业务执行未实现，不能用本面板自己的登录机制来冒充。

## 传输与发布

原生 TLS、源 CIDR 限制、强制非回环 TLS 开关均有代码；服务器尚无已配置证书，当前 HTTP 风险仍存在。部署没有改云防火墙、安装 nginx/caddy、添加 CA、重启 daemon、应用补丁或修改现场网关。

公开快照仍按手工挑选/脱敏/发布，不自动同步或推送。目录卫生检查不等同完整安全审计。公开首发仍需用户决定发布目标。
