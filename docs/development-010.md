# 0.10 高级本地管理

本版继续使用 daemon 官方 Console API，不伪造身份、权益或云端功能。

> 历史记录更正：0.10 只接入了相关接口，不代表浏览器写入/下载闭环已验收。后续复查发现 Skill 404 被转成 502、Artifact 动态按钮未绑定、分页未贯通 UI，以及网关重启配置不兼容。上述问题在 [0.11 修复记录](development-011.md) 中分别验证；以下是当时的功能范围，不是当前验收结论。

## 新增

- `/api/v1/system/status` 的白名单字段仪表盘，不把 instanceId、restartCommand 和 productLinks 返回浏览器。
- Waker 级 Console Session 分页/来源筛选、未读标记、管理员确认删除。
- Session Artifact 白名单清单和 32 MiB 上限下载。下载 ID 禁止路径字符；HTML/SVG 强制 `application/octet-stream` 和 attachment，不提供本机任意路径读取。
- Skill 正文与 `baseVersionId` 保存；官方 409 映射成可读冲突。版本、diff 与有原因的回滚均接入。内置/不可变 Skill 正文 404 当时仍被错误转换成 502；0.11 才修正为不可编辑状态。
- Automation 列表、运行历史、启停、管理员确认的 run-now/delete。run-now 纳入消费限频并提示异步核对，避免盲目重试。
- IM pending pairing 列表与最小权限批准：Waker 由管理员选择，`allowQoderwakeCommands=false`、`allowCustomModel=false`。

## 真实只读契约检查

服务器 1.1.6 对 health、system status、triggers、pending pairing、console sessions 和 artifacts 均返回预期 envelope。现有环境中：2 个 Waker；样本会话 Artifact 数据为空；首个内置 Skill 正文为 404、版本列表为空。没有为验收执行自动化、删除会话、批准 IM、修改 Skill 或下载第三方插件。

## 保留边界

- 还没有 Trigger 创建编辑器、Webhook probe、本地插件 ZIP 或 Global Settings 编辑；这些需要更严格 schema/SSRF/第三方代码边界。
- Artifact 下载只给 admin，不对 viewer/API caller 开放。
- 会话和自动化破坏性动作不提供批量入口。
