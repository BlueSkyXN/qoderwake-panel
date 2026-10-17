# Provider 运行兼容

QoderWake 1.1.12 的 `group_qa_assistant` 会话强制传入 `--setting-sources local`。自有 Provider 保存在用户级 `qodercli/settings.json` 时，模型目录能列出模型，但会话启动后无法读取 Provider，最终报 `ModelNotFoundError`。此时请求尚未发到模型服务。

`ops/qodercli-provider-bridge.py` 是可选的 Linux 启动兼容工具。Panel 升级不会自动安装它。只有 `QODER_SDK_CUSTOM_BASE_URL_BYOK=1`、设置了 `QODER_CONFIG_DIR`，且 `--setting-sources` 排除了 `user` 时才补读 Provider。原有 `permissions`、`security`、hooks 等用户设置不被导入；项目、本地和 `--settings` 中显式配置的 Provider 仍按原优先级覆盖。

工具在每次启动时读取原配置，将合并结果写入匿名内存文件，经 `--settings /proc/self/fd/...` 交给原 CLI。Provider 密钥不写入参数、日志或 Waker 工作区；修改 Provider 后，新启动的会话读取新值。配置须为有效 JSON；读取失败时会停止此次启动并返回不含配置内容的错误码。

## 安装与回滚

先确认错误对应上述设置来源，记录 worker 在 `/proc/<pid>/exe` 中实际执行的 CLI 路径和 SHA-256。SDK 会话可能使用 daemon HOME 的 `runtime-generations/<代际>/qodercli/`；全局安装目录中的同名 CLI 不一定是会话入口。使用维护窗口与原有受管控制器停止 daemon，确认没有运行中的任务，再操作该精确文件。以下 `CLI` 必须是已核对的真实文件，不能是符号链接。

```bash
CLI=/path/to/qodercli-cn-wake
test -f "$CLI" && test ! -L "$CLI" && test ! -e "$CLI.original"
sha256sum "$CLI"
cp -p "$CLI" "$CLI.original"
install -m 755 ops/qodercli-provider-bridge.py "$CLI.provider-bridge-new"
mv "$CLI.provider-bridge-new" "$CLI"
"$CLI" --version
sha256sum "$CLI.original"
```

逐项执行并检查结果，原件的 SHA-256 必须与安装前相同。然后用原有入口启动 daemon，创建真实群聊助理会话并显式选择自有模型，核对日志中的模型路由与回复。需要记忆能力时，另查真实 `memory_recall` 工具调用及其结果。

回滚时先停止 daemon，再把已核对 SHA-256 的 `.original` 移回原 CLI 路径，之后启动并回读健康状态。不要在有运行任务时替换 CLI，不要覆盖已有 `.original`。

## 官方升级之后

官方升级可能选择新的 CLI 路径或替换入口；每次升级均需重新核查实际路径、版本和设置来源。先验证新版是否已经支持该场景，再决定是否安装兼容工具。旧 `.original` 属于原来的官方版本，不能拿来覆盖新版 CLI。

此工具只修复本机 Provider 配置遗漏。上游模型的内容拒答、token 统计及协议实现仍以实际模型服务为准。
