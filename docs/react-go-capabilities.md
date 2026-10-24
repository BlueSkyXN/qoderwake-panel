# 能力与接口覆盖

本表以迁移基线的 `panel/qoderwake-panel.py`、CLI 目录 `panel/config/cli-reference.json` 和 Go owning handlers 为依据。101 个旧 `(method, path)` 分派入口均有 Go 对应实现；373 项 CLI 操作保留原目录、参数与权限信息。分派覆盖表示入口和责任模块齐全，行为兼容由模块测试、Python 对照与平台演练共同验证。

## 功能与验证入口

| 能力 | React 页面 | Go 实现与验证 |
| --- | --- | --- |
| 账号、RBAC、Waker 范围、会话撤销 | Users / Account / SessionProvider | `internal/auth`、`server/server_test.go`：旧账号和 Cookie、权限刷新、CSRF、最后管理员、并发更新 |
| 技能正文、版本、diff、回滚 | Skills | `server/state_test.go`、`server/server_test.go`：范围裁剪、版本冲突、保存和回滚 |
| Waker、模型和 Provider | Wakers / Models | `internal/provider`、`server/providers_test.go`：CAS、密钥操作、多模型 JSON、未知字段、探针、模型绑定与回读 |
| 删除预检与结果核对 | shared Deletion | `internal/deletion`、`server/deletions_test.go`：依赖、TTL、主体绑定、幂等、unknown 与核对 |
| 完整 CLI、交互输入、上传与输出文件 | Native | `internal/cli`、`web/tests`：373 项目录、实时 help、参数构造、权限、PTY、文件引用、超时与重启不重放 |
| 聊天、会话、产物与记忆 | Chat / Sessions / Memory | `server/conversations_test.go`：资源归属、消息、产物、分页与部分失败 |
| 自动化、历史、配对 | Operations | `server/conversations_test.go`：Waker 范围、变更权限、操作结果 |
| 插件与通道 | Plugins / Channels | `server/integrations_test.go`：插件版本、开关、配置与敏感字段保留 |
| caller、审计、备份与遥测 | Callers / Gateway / Audit / Security | `internal/auth`、`server/backups_test.go`、`server/server_test.go`：配额、撤销、归档边界与配置 |
| 增量用量 | Usage | `internal/usage/store_test.go`：Python → Go → Python 游标互通、重复与截断日志 |
| daemon、维护排空、文件校验、冻结补丁 | Controls | `internal/platform`、`managed`、`patches`、`service`、`server/runtime_test.go`：身份、生命周期、撤权与恢复 |
| 网关、策略、日志、出站连接、Linux firewall | Network | `internal/gateway`、`server/network_test.go`：策略对照、SSE、代际切换、回退、中断恢复、旧网关兼容 |

文件名均相对于 `internal/`，已带 `internal/` 的路径除外。网络测试使用隔离网关、合成上游或 fake executor；正式防火墙写入和官方付费调用留给单独的上线验收。真实实例已验证版本、资源读取、目录和 `status`，未逐项执行 373 个真实操作。

## 旧 HTTP 入口映射

路径和函数均相对于 `qoderwake-panel/`。特殊登录分派在 `ServeHTTP/login`，插件在 `pluginGet`；其余按 GET/POST 和 Native handler 显式分派。

| Method | Path | Go handler |
| --- | --- | --- |
| GET | `/api/audit` | `internal/server/server.go` · `get` |
| GET | `/api/auth/mode` | `internal/server/server.go` · `ServeHTTP` |
| GET | `/api/automation/runs` | `internal/server/server.go` · `get` |
| GET | `/api/automations` | `internal/server/server.go` · `get` |
| GET | `/api/backups` | `internal/server/server.go` · `get` |
| GET | `/api/callers` | `internal/server/server.go` · `get` |
| GET | `/api/channels` | `internal/server/server.go` · `get` |
| GET | `/api/channels/detail` | `internal/server/server.go` · `get` |
| GET | `/api/chat/messages` | `internal/server/server.go` · `get` |
| GET | `/api/chat/sessions` | `internal/server/server.go` · `get` |
| GET | `/api/egress` | `internal/server/server.go` · `get` |
| GET | `/api/file-check` | `internal/server/server.go` · `get` |
| GET | `/api/health` | `internal/server/server.go` · `ServeHTTP` |
| GET | `/api/maintenance` | `internal/server/server.go` · `get` |
| GET | `/api/memory` | `internal/server/server.go` · `get` |
| GET | `/api/native/catalog` | `internal/server/native.go` · `nativeGet` |
| GET | `/api/native/file` | `internal/server/native.go` · `nativeGet` |
| GET | `/api/native/files` | `internal/server/native.go` · `nativeGet` |
| GET | `/api/native/job` | `internal/server/native.go` · `nativeGet` |
| GET | `/api/native/jobs` | `internal/server/native.go` · `nativeGet` |
| GET | `/api/native/operation` | `internal/server/native.go` · `nativeGet` |
| GET | `/api/net/state` | `internal/server/server.go` · `get` |
| GET | `/api/pairings` | `internal/server/server.go` · `get` |
| GET | `/api/patch/state` | `internal/server/server.go` · `get` |
| GET | `/api/plugins` | `internal/server/integrations.go` · `pluginGet` |
| GET | `/api/plugins/catalog` | `internal/server/integrations.go` · `pluginGet` |
| GET | `/api/plugins/detail` | `internal/server/integrations.go` · `pluginGet` |
| GET | `/api/plugins/installations` | `internal/server/integrations.go` · `pluginGet` |
| GET | `/api/plugins/installed` | `internal/server/integrations.go` · `pluginGet` |
| GET | `/api/runtime` | `internal/server/server.go` · `get` |
| GET | `/api/session/artifact-file` | `internal/server/server.go` · `get` |
| GET | `/api/session/artifacts` | `internal/server/server.go` · `get` |
| GET | `/api/sessions/query` | `internal/server/server.go` · `get` |
| GET | `/api/skill/content` | `internal/server/server.go` · `get` |
| GET | `/api/skill/diff` | `internal/server/server.go` · `get` |
| GET | `/api/skill/versions` | `internal/server/server.go` · `get` |
| GET | `/api/skills` | `internal/server/server.go` · `get` |
| GET | `/api/state` | `internal/server/server.go` · `get` |
| GET | `/api/system/status` | `internal/server/server.go` · `get` |
| GET | `/api/usage` | `internal/server/server.go` · `get` |
| GET | `/api/users` | `internal/server/server.go` · `get` |
| GET | `/api/waker/detail` | `internal/server/server.go` · `get` |
| GET | `/api/whoami` | `internal/server/server.go` · `ServeHTTP` |
| POST | `/api/account/password` | `internal/server/server.go` · `post` |
| POST | `/api/apply` | `internal/server/server.go` · `post` |
| POST | `/api/automation/delete` | `internal/server/server.go` · `post` |
| POST | `/api/automation/run` | `internal/server/server.go` · `post` |
| POST | `/api/automation/toggle` | `internal/server/server.go` · `post` |
| POST | `/api/backup` | `internal/server/server.go` · `post` |
| POST | `/api/backup/delete` | `internal/server/server.go` · `post` |
| POST | `/api/callers/create` | `internal/server/server.go` · `post` |
| POST | `/api/callers/revoke` | `internal/server/server.go` · `post` |
| POST | `/api/channels/action` | `internal/server/server.go` · `post` |
| POST | `/api/channels/config` | `internal/server/server.go` · `post` |
| POST | `/api/channels/delete` | `internal/server/server.go` · `post` |
| POST | `/api/chat/new` | `internal/server/server.go` · `post` |
| POST | `/api/chat/send` | `internal/server/server.go` · `post` |
| POST | `/api/deletion/preview` | `internal/server/server.go` · `post` |
| POST | `/api/deletion/reconcile` | `internal/server/server.go` · `post` |
| POST | `/api/deletion/release` | `internal/server/server.go` · `post` |
| POST | `/api/deletion/status` | `internal/server/server.go` · `post` |
| POST | `/api/file-check` | `internal/server/server.go` · `post` |
| POST | `/api/gw` | `internal/server/server.go` · `post` |
| POST | `/api/login` | `internal/server/server.go` · `login` |
| POST | `/api/logout` | `internal/server/server.go` · `ServeHTTP` |
| POST | `/api/maintenance/enter` | `internal/server/server.go` · `post` |
| POST | `/api/maintenance/exit` | `internal/server/server.go` · `post` |
| POST | `/api/migrate` | `internal/server/server.go` · `login` |
| POST | `/api/native/cancel` | `internal/server/native.go` · `nativePost` |
| POST | `/api/native/execute` | `internal/server/native.go` · `nativePost` |
| POST | `/api/native/file/append` | `internal/server/native.go` · `nativePost` |
| POST | `/api/native/file/create` | `internal/server/native.go` · `nativePost` |
| POST | `/api/native/file/delete` | `internal/server/native.go` · `nativePost` |
| POST | `/api/native/input` | `internal/server/native.go` · `nativePost` |
| POST | `/api/net/config` | `internal/server/server.go` · `post` |
| POST | `/api/net/firewall` | `internal/server/server.go` · `post` |
| POST | `/api/pairing/approve` | `internal/server/server.go` · `post` |
| POST | `/api/patch/execute` | `internal/server/server.go` · `post` |
| POST | `/api/patch/plan` | `internal/server/server.go` · `post` |
| POST | `/api/plugins/install` | `internal/server/server.go` · `post` |
| POST | `/api/plugins/remove` | `internal/server/server.go` · `post` |
| POST | `/api/plugins/toggle` | `internal/server/server.go` · `post` |
| GET / POST | `/api/provider/config` | `internal/server/model_settings.go` · 明文多模型编辑 |
| GET | `/api/models/native-byok` | `internal/server/model_settings.go` · 原生访问状态 |
| GET | `/api/waker/models` | `internal/server/model_settings.go` · 偏好与会话模型 |
| POST | `/api/waker/models/update` | `internal/server/model_settings.go` · 分范围保存与回读 |
| POST | `/api/preference` | `internal/server/server.go` · `post` |
| POST | `/api/provider` | `internal/server/server.go` · `post` |
| POST | `/api/provider/delete` | `internal/server/server.go` · `post` |
| POST | `/api/provider/probe-plan` | `internal/server/server.go` · `post` |
| POST | `/api/runtime/apply` | `internal/server/server.go` · `post` |
| POST | `/api/runtime/policy` | `internal/server/server.go` · `post` |
| POST | `/api/session/delete` | `internal/server/server.go` · `post` |
| POST | `/api/session/read` | `internal/server/server.go` · `post` |
| POST | `/api/skill/rollback` | `internal/server/server.go` · `post` |
| POST | `/api/skill/update` | `internal/server/server.go` · `post` |
| POST | `/api/telemetry` | `internal/server/server.go` · `post` |
| POST | `/api/test` | `internal/server/server.go` · `post` |
| POST | `/api/test/saved` | `internal/server/server.go` · `post` |
| POST | `/api/users/create` | `internal/server/server.go` · `post` |
| POST | `/api/users/password` | `internal/server/server.go` · `post` |
| POST | `/api/users/update` | `internal/server/server.go` · `post` |
| POST | `/api/waker` | `internal/server/server.go` · `post` |
| POST | `/api/waker/delete` | `internal/server/server.go` · `post` |
| POST | `/api/waker/update` | `internal/server/server.go` · `post` |

模型配置的使用方式、存储边界和保存结果见 [模型配置](model-management.md)。
