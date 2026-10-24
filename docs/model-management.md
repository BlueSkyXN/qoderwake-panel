# 模型配置

管理员在“模型配置”页面维护明文 Provider 和 Waker 模型偏好。已分配 Waker 的维护者、观察者可以在“机器人”页面查看该 Waker 的偏好和会话模型；Provider 配置与偏好写入仍由管理员管理。

## 明文 BYOK

现有 Provider 的“编辑 JSON”支持完整的多模型列表，以及 `protocol`、`authType`、`contextWindow`、`maxOutputTokens`、`capabilities.thinking` 和其他扩展字段。编辑器使用原始 JSON 数字文本，避免大整数在浏览器解析后失真。

保存位置是 Wake 数据目录下的 `qodercli/settings.json`。API Key 和凭据字段显示为 `__PANEL_KEEP_SECRET__`；通过“保留当前值”“更换”“删除”操作 API Key。更换时可以显示新输入，已有密钥不回传到浏览器。JSON 中遗漏的凭据字段会保留原值，直接修改占位符会报错。

保存前比较文件版本，并在本机文件锁内原子替换。数据文件与单份恢复文件使用 `0600` 权限；恢复文件位于 Panel 数据目录的 `provider-settings.previous.json`。保存后回读配置，若有冲突或无法确认结果，需先读取最新配置再继续。

已有非 loopback HTTP 地址可以原样保留；修改地址沿用现有地址校验规则。编辑不会自动替换协议，也不会触发 Provider 请求。删除或重命名模型会检查 Waker、运行任务等依赖；有引用或检查不完整时拒绝保存，有历史记录时要求明确确认。

## Wake 原生 BYOK

“Wake 原生 BYOK”独立读取官方 `/api/models/byok`，显示可读、仅限本机界面、接口不可用等状态。管理入口为运行 Wake 的电脑上的官方客户端模型设置。

当前 Panel 使用官方 CLI frontend session。Wake 返回 `MODEL_SETTINGS_LOCAL_ONLY` 时，Panel 不提供原生写入，也不把操作转存到明文文件。原生凭据保存、加密和迁移由官方本机界面完成；此页面的可读状态不用于判断加密是否已经落盘，也不会因模型目录包含旧 Provider 就标记迁移完成。

## Waker 模型绑定

页面分开显示三类信息：

- 无项目默认：`model-preferences.noProject`。
- 项目覆盖：`model-preferences.byProject`，按项目 ID 添加或更新。
- 会话实际模型：`currentModel`、`currentModelRuntimeConfig`、`modelConfigRevision` 和 `runtimeRestartRequired`。

模型偏好经官方 `PUT /api/model-preferences/:wakerId/entry` 保存。请求包含模型及选定的项目范围，可设置推理强度和上下文窗口；参数留空时保留该范围已有参数。当前界面支持添加、更新项目偏好，未提供删除覆盖或清空参数操作。

Panel 在保存前比较完整偏好的版本，保存后再核对目标范围中的模型和运行参数。版本检查覆盖 Panel 内的并发修改；官方接口没有提供原子版本条件，其他官方客户端也可能同时写入，因此回读是必要步骤。

写入超时、回读失败或值不一致时显示“待核对”，不自动重试写入，审计记录不会标记为已确认成功。已有会话继续展示 Wake 返回的实际模型与重启状态。这里的保存和回读验证配置，不发送模型推理请求。
