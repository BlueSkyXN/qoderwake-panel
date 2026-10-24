# 官方 CLI 功能覆盖清单

目标参考：国际版 QoderWake 1.1.13，macOS arm64。采集日期：2026-10-23。

命令参考来自目标二进制逐层 `--help`，对应 [结构化来源](../panel/config/cli-reference.json) 与 [权限登记](../panel/config/cli-policy.json)。共 49 个顶层入口、482 个节点、373 个可执行操作、1,817 个选项、116 个位置参数。

所有条目均有操作表单。Waker 删除与 Skill 回滚复用已有 HTTP 流程；其他操作通过受控 CLI 适配执行。原有页面继续使用其 HTTP / 专用运维适配。完整参数保存在来源中，界面逐项呈现。

“已接入”表示已实现入口、参数传递、权限及执行器；真实业务写入的验收需使用专门的测试资源，帮助采集和模拟执行不作为生产写入验收。

| 操作 | 作用 | 权限 | 执行方式 |
|---|---|---|---|
| `start` | 可能写入 / 外部动作 | 管理员 | CLI |
| `stop` | 可能写入 / 外部动作 | 管理员 | CLI |
| `restart` | 可能写入 / 外部动作 | 管理员 | CLI |
| `status` | 读取 | 管理员 | CLI |
| `readiness` | 读取 | 管理员 | CLI |
| `portal` | 可能写入 / 外部动作 | 管理员 | CLI |
| `login` | 可能写入 / 外部动作 | 管理员 | CLI |
| `logout` | 可能写入 / 外部动作 | 管理员 | CLI |
| `whoami` | 读取 | 管理员 | CLI |
| `version` | 读取 | 管理员 | CLI |
| `update` | 可能写入 / 外部动作 | 管理员 | CLI |
| `log` | 读取 | 管理员 | CLI |
| `feedback` | 可能写入 / 外部动作 | 管理员 | CLI |
| `config list` | 读取 | 管理员 | CLI |
| `config get` | 读取 | 管理员 | CLI |
| `config set` | 可能写入 / 外部动作 | 管理员 | CLI |
| `config reset` | 可能写入 / 外部动作 | 管理员 | CLI |
| `proxy status` | 读取 | 管理员 | CLI |
| `proxy set` | 可能写入 / 外部动作 | 管理员 | CLI |
| `proxy reset` | 可能写入 / 外部动作 | 管理员 | CLI |
| `settings init` | 可能写入 / 外部动作 | 管理员 | CLI |
| `settings get` | 读取 | 管理员 | CLI |
| `settings set` | 可能写入 / 外部动作 | 管理员 | CLI |
| `storage migrate` | 可能写入 / 外部动作 | 管理员 | CLI |
| `backup list` | 读取 | 管理员 | CLI |
| `backup restore` | 可能写入 / 外部动作 | 管理员 | CLI |
| `plugin list` | 读取 | 管理员 | CLI |
| `plugin info` | 读取 | 管理员 | CLI |
| `plugin install` | 可能写入 / 外部动作 | 管理员 | CLI |
| `plugin enable` | 可能写入 / 外部动作 | 管理员 | CLI |
| `plugin disable` | 可能写入 / 外部动作 | 管理员 | CLI |
| `plugin uninstall` | 可能写入 / 外部动作 | 管理员 | CLI |
| `plugin reload` | 可能写入 / 外部动作 | 管理员 | CLI |
| `plugin events` | 读取 | 管理员 | CLI |
| `waker list` | 读取 | 管理员 | CLI |
| `waker get` | 读取 | 所属 Waker 的维护者、观察者 | CLI |
| `waker delete-impact` | 读取 | 管理员 | CLI |
| `waker context` | 读取 | 所属 Waker 的维护者、观察者 | CLI |
| `waker create` | 可能写入 / 外部动作 | 管理员 | CLI |
| `waker export` | 可能写入 / 外部动作 | 管理员 | CLI |
| `waker delete` | 可能写入 / 外部动作 | 管理员 | 原有 HTTP 删除预检 |
| `project list` | 读取 | 所属 Waker 的维护者、观察者 | CLI |
| `project create` | 可能写入 / 外部动作 | 管理员 | CLI |
| `project get` | 读取 | 所属 Waker 的维护者、观察者 | CLI |
| `project update` | 可能写入 / 外部动作 | 管理员 | CLI |
| `project delete` | 可能写入 / 外部动作 | 管理员 | CLI |
| `project onboard` | 可能写入 / 外部动作 | 管理员 | CLI |
| `mcp list` | 读取 | 所属 Waker 的维护者、观察者 | CLI |
| `mcp get` | 读取 | 所属 Waker 的维护者、观察者 | CLI |
| `mcp add` | 可能写入 / 外部动作 | 管理员 | CLI |
| `mcp update` | 可能写入 / 外部动作 | 管理员 | CLI |
| `mcp toggle` | 可能写入 / 外部动作 | 管理员 | CLI |
| `mcp delete` | 可能写入 / 外部动作 | 管理员 | CLI |
| `mcp refresh-tools` | 可能写入 / 外部动作 | 管理员 | CLI |
| `mcp operation` | 读取 | 管理员 | CLI |
| `skill list` | 读取 | 所属 Waker 的维护者、观察者 | CLI |
| `skill manage` | 可能写入 / 外部动作 | 所属 Waker 的维护者 | CLI |
| `skill add` | 可能写入 / 外部动作 | 管理员 | CLI |
| `skill upload` | 可能写入 / 外部动作 | 管理员 | CLI |
| `skill versions` | 读取 | 所属 Waker 的维护者、观察者 | CLI |
| `skill diff` | 读取 | 所属 Waker 的维护者、观察者 | CLI |
| `skill rollback` | 可能写入 / 外部动作 | 管理员 | 原有 HTTP 版本回滚 |
| `skill toggle` | 可能写入 / 外部动作 | 所属 Waker 的维护者 | CLI |
| `skill self-evolution` | 可能写入 / 外部动作 | 所属 Waker 的维护者 | CLI |
| `skill delete` | 可能写入 / 外部动作 | 管理员 | CLI |
| `skill install` | 可能写入 / 外部动作 | 管理员 | CLI |
| `skill search` | 读取 | 管理员 | CLI |
| `permission get` | 读取 | 所属 Waker 的维护者、观察者 | CLI |
| `permission update` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel settings` | 读取 | 管理员 | CLI |
| `channel list` | 读取 | 管理员 | CLI |
| `channel get` | 读取 | 管理员 | CLI |
| `channel config` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel start` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel stop` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel restart` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel delete` | 可能写入 / 外部动作 | 管理员 | CLI |
| `extension list` | 读取 | 管理员 | CLI |
| `session list` | 读取 | 管理员 | CLI |
| `session artifacts` | 读取 | 管理员 | CLI |
| `session detail` | 读取 | 管理员 | CLI |
| `session create` | 可能写入 / 外部动作 | 管理员 | CLI |
| `session send` | 可能写入 / 外部动作 | 管理员 | CLI |
| `session interrupt` | 可能写入 / 外部动作 | 管理员 | CLI |
| `session respond` | 可能写入 / 外部动作 | 管理员 | CLI |
| `session wait` | 读取 | 管理员 | CLI |
| `session set-model` | 可能写入 / 外部动作 | 管理员 | CLI |
| `session stop` | 可能写入 / 外部动作 | 管理员 | CLI |
| `session delete` | 可能写入 / 外部动作 | 管理员 | CLI |
| `session rename` | 可能写入 / 外部动作 | 管理员 | CLI |
| `session events` | 读取 | 管理员 | CLI |
| `session stream` | 读取 | 管理员 | CLI |
| `session trajectory` | 可能写入 / 外部动作 | 管理员 | CLI |
| `group create` | 可能写入 / 外部动作 | 管理员 | CLI |
| `group list` | 读取 | 管理员 | CLI |
| `group show` | 读取 | 管理员 | CLI |
| `group rename` | 可能写入 / 外部动作 | 管理员 | CLI |
| `group add-waker` | 可能写入 / 外部动作 | 管理员 | CLI |
| `sop list` | 读取 | 管理员 | CLI |
| `sop publish` | 可能写入 / 外部动作 | 管理员 | CLI |
| `sop init` | 可能写入 / 外部动作 | 管理员 | CLI |
| `sop validate` | 读取 | 管理员 | CLI |
| `sop build` | 可能写入 / 外部动作 | 管理员 | CLI |
| `messages list` | 读取 | 管理员 | CLI |
| `messages send` | 可能写入 / 外部动作 | 管理员 | CLI |
| `messages claim` | 可能写入 / 外部动作 | 管理员 | CLI |
| `messages read` | 可能写入 / 外部动作 | 管理员 | CLI |
| `todo list` | 读取 | 管理员 | CLI |
| `todo add` | 可能写入 / 外部动作 | 管理员 | CLI |
| `todo update` | 可能写入 / 外部动作 | 管理员 | CLI |
| `goal get` | 读取 | 管理员 | CLI |
| `goal mutate` | 可能写入 / 外部动作 | 管理员 | CLI |
| `runs list` | 读取 | 管理员 | CLI |
| `runs cancel` | 可能写入 / 外部动作 | 管理员 | CLI |
| `trace list` | 读取 | 管理员 | CLI |
| `trace show` | 读取 | 管理员 | CLI |
| `automation list` | 读取 | 管理员 | CLI |
| `automation detail` | 读取 | 管理员 | CLI |
| `automation sources` | 读取 | 管理员 | CLI |
| `automation api-endpoint` | 可能写入 / 外部动作 | 管理员 | CLI |
| `automation test-pull` | 可能写入 / 外部动作 | 管理员 | CLI |
| `automation run-now` | 可能写入 / 外部动作 | 管理员 | CLI |
| `automation update` | 可能写入 / 外部动作 | 管理员 | CLI |
| `automation delete` | 可能写入 / 外部动作 | 管理员 | CLI |
| `automation stats` | 读取 | 管理员 | CLI |
| `automation calendar` | 读取 | 管理员 | CLI |
| `automation runs` | 读取 | 管理员 | CLI |
| `automation invoke` | 可能写入 / 外部动作 | 管理员 | CLI |
| `memory show` | 读取 | 所属 Waker 的维护者、观察者 | CLI |
| `memory dream` | 可能写入 / 外部动作 | 管理员 | CLI |
| `memory update` | 可能写入 / 外部动作 | 管理员 | CLI |
| `memory remove` | 可能写入 / 外部动作 | 管理员 | CLI |
| `memory snapshot` | 可能写入 / 外部动作 | 管理员 | CLI |
| `memory versions` | 读取 | 所属 Waker 的维护者、观察者 | CLI |
| `memory diff` | 读取 | 所属 Waker 的维护者、观察者 | CLI |
| `memory rollback` | 可能写入 / 外部动作 | 管理员 | CLI |
| `memory export` | 可能写入 / 外部动作 | 管理员 | CLI |
| `memory import` | 可能写入 / 外部动作 | 管理员 | CLI |
| `teams diagnose` | 读取 | 管理员 | CLI |
| `teams mission-workroom` | 读取 | 管理员 | CLI |
| `workflow list` | 读取 | 管理员 | CLI |
| `workflow get` | 读取 | 管理员 | CLI |
| `workflow create` | 可能写入 / 外部动作 | 管理员 | CLI |
| `workflow update` | 可能写入 / 外部动作 | 管理员 | CLI |
| `workflow set-meta` | 可能写入 / 外部动作 | 管理员 | CLI |
| `workflow delete` | 可能写入 / 外部动作 | 管理员 | CLI |
| `workflow run` | 可能写入 / 外部动作 | 管理员 | CLI |
| `workflow runs` | 读取 | 管理员 | CLI |
| `workflow run-get` | 读取 | 管理员 | CLI |
| `workflow run-terminate` | 可能写入 / 外部动作 | 管理员 | CLI |
| `workflow run-retry` | 可能写入 / 外部动作 | 管理员 | CLI |
| `workflow versions` | 读取 | 管理员 | CLI |
| `workflow rollback` | 可能写入 / 外部动作 | 管理员 | CLI |
| `workflow confirmations` | 读取 | 管理员 | CLI |
| `workflow confirm` | 可能写入 / 外部动作 | 管理员 | CLI |
| `workflow skip` | 可能写入 / 外部动作 | 管理员 | CLI |
| `models list` | 读取 | 管理员 | CLI |
| `account profile` | 读取 | 管理员 | CLI |
| `account plan` | 读取 | 管理员 | CLI |
| `account usage` | 读取 | 管理员 | CLI |
| `account activity` | 读取 | 管理员 | CLI |
| `connector market` | 读取 | 管理员 | CLI |
| `board tasks` | 读取 | 管理员 | CLI |
| `board task` | 读取 | 管理员 | CLI |
| `board summary` | 读取 | 管理员 | CLI |
| `board filter-options` | 读取 | 管理员 | CLI |
| `human-action list` | 读取 | 管理员 | CLI |
| `human-action ignore` | 可能写入 / 外部动作 | 管理员 | CLI |
| `knowledge featured` | 读取 | 管理员 | CLI |
| `diagnostics network` | 可能写入 / 外部动作 | 管理员 | CLI |
| `settings global get` | 读取 | 管理员 | CLI |
| `settings global set` | 可能写入 / 外部动作 | 管理员 | CLI |
| `settings language get` | 读取 | 管理员 | CLI |
| `settings language set` | 可能写入 / 外部动作 | 管理员 | CLI |
| `plugin scheduled-task list` | 读取 | 管理员 | CLI |
| `plugin scheduled-task register` | 可能写入 / 外部动作 | 管理员 | CLI |
| `plugin scheduled-task unregister` | 可能写入 / 外部动作 | 管理员 | CLI |
| `waker template list` | 读取 | 管理员 | CLI |
| `waker template save` | 可能写入 / 外部动作 | 管理员 | CLI |
| `waker template show` | 读取 | 管理员 | CLI |
| `waker template versions` | 读取 | 管理员 | CLI |
| `waker template instantiate` | 可能写入 / 外部动作 | 管理员 | CLI |
| `waker template update` | 可能写入 / 外部动作 | 管理员 | CLI |
| `waker template rollback` | 可能写入 / 外部动作 | 管理员 | CLI |
| `waker template delete` | 可能写入 / 外部动作 | 管理员 | CLI |
| `waker update name` | 可能写入 / 外部动作 | 所属 Waker 的维护者 | CLI |
| `waker update description` | 可能写入 / 外部动作 | 所属 Waker 的维护者 | CLI |
| `waker update avatar` | 可能写入 / 外部动作 | 管理员 | CLI |
| `waker update skill` | 可能写入 / 外部动作 | 管理员 | CLI |
| `waker update mcp` | 可能写入 / 外部动作 | 管理员 | CLI |
| `waker update identity` | 可能写入 / 外部动作 | 所属 Waker 的维护者 | CLI |
| `waker update persona` | 可能写入 / 外部动作 | 所属 Waker 的维护者 | CLI |
| `waker update bible` | 可能写入 / 外部动作 | 所属 Waker 的维护者 | CLI |
| `waker update from-file` | 可能写入 / 外部动作 | 管理员 | CLI |
| `local employee list` | 读取 | 管理员 | CLI |
| `mcp auth start` | 可能写入 / 外部动作 | 管理员 | CLI |
| `permission patch tool-guard` | 可能写入 / 外部动作 | 管理员 | CLI |
| `permission patch file-guard` | 可能写入 / 外部动作 | 管理员 | CLI |
| `permission patch builtin-tools` | 可能写入 / 外部动作 | 管理员 | CLI |
| `permission patch model-security` | 可能写入 / 外部动作 | 管理员 | CLI |
| `permission builtin tool-guard-rules` | 读取 | 管理员 | CLI |
| `permission builtin tool-catalog` | 读取 | 管理员 | CLI |
| `permission approval list` | 读取 | 管理员 | CLI |
| `permission approval watch` | 读取 | 管理员 | CLI |
| `permission approval resolve` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel dingtalk setup` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel dingtalk qr-connect` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel dingtalk qr-start` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel dingtalk qr-poll` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel aliding setup` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel aliding qr-start` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel aliding qr-poll` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel dingtalk-ai-assistance setup` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel feishu setup` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel feishu qr-connect` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel feishu status` | 读取 | 管理员 | CLI |
| `channel feishu qr-start` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel feishu qr-poll` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel feishu validate` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel lark setup` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel lark qr-connect` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel lark qr-start` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel lark qr-poll` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel lark validate` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel qq-bot setup` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel slack setup` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel msteams setup` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel whatsapp setup` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel whatsapp connect` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel whatsapp status` | 读取 | 管理员 | CLI |
| `channel whatsapp relink` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel whatsapp logout` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel weixin setup` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel weixin qr-connect` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel weixin qr-start` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel weixin qr-poll` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel wecom-bot setup` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel wecom-bot qr-connect` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel wecom-bot qr-start` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel wecom-bot qr-poll` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel wecom-bot status` | 读取 | 管理员 | CLI |
| `channel dingtalk-user setup` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel pairing generate-code` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel pairing list` | 读取 | 管理员 | CLI |
| `channel pairing get` | 读取 | 管理员 | CLI |
| `channel pairing pending` | 读取 | 管理员 | CLI |
| `channel pairing delete` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel pairing approve` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel pairing ignore` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel pairing manual-add` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel pairing toggle-targets` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel pairing delete-by-target` | 可能写入 / 外部动作 | 管理员 | CLI |
| `session artifact upload` | 可能写入 / 外部动作 | 管理员 | CLI |
| `session artifact download` | 可能写入 / 外部动作 | 管理员 | CLI |
| `session workspace set` | 可能写入 / 外部动作 | 管理员 | CLI |
| `group task create` | 可能写入 / 外部动作 | 管理员 | CLI |
| `group task rename` | 可能写入 / 外部动作 | 管理员 | CLI |
| `group member set-model` | 可能写入 / 外部动作 | 管理员 | CLI |
| `group skill upload` | 可能写入 / 外部动作 | 管理员 | CLI |
| `group skill list` | 读取 | 管理员 | CLI |
| `group skill add` | 可能写入 / 外部动作 | 管理员 | CLI |
| `group skill delete` | 可能写入 / 外部动作 | 管理员 | CLI |
| `group sop list` | 读取 | 管理员 | CLI |
| `group sop set` | 可能写入 / 外部动作 | 管理员 | CLI |
| `group sop clear` | 可能写入 / 外部动作 | 管理员 | CLI |
| `messages attachment download` | 可能写入 / 外部动作 | 管理员 | CLI |
| `atwaker topics list` | 读取 | 管理员 | CLI |
| `atwaker topics search` | 读取 | 管理员 | CLI |
| `atwaker topics get` | 读取 | 管理员 | CLI |
| `atwaker topics upsert` | 可能写入 / 外部动作 | 管理员 | CLI |
| `atwaker tasks search` | 读取 | 管理员 | CLI |
| `atwaker tasks get` | 读取 | 管理员 | CLI |
| `atwaker tasks update` | 可能写入 / 外部动作 | 管理员 | CLI |
| `atwaker tasks status` | 可能写入 / 外部动作 | 管理员 | CLI |
| `atwaker tasks summary` | 读取 | 管理员 | CLI |
| `atwaker tasks progress` | 可能写入 / 外部动作 | 管理员 | CLI |
| `atwaker memory types` | 读取 | 管理员 | CLI |
| `atwaker memory recall` | 读取 | 管理员 | CLI |
| `atwaker memory get` | 读取 | 管理员 | CLI |
| `atwaker memory create` | 可能写入 / 外部动作 | 管理员 | CLI |
| `atwaker memory update` | 可能写入 / 外部动作 | 管理员 | CLI |
| `atwaker memory delete` | 可能写入 / 外部动作 | 管理员 | CLI |
| `atwaker memory evidence` | 可能写入 / 外部动作 | 管理员 | CLI |
| `atwaker memory activate` | 可能写入 / 外部动作 | 管理员 | CLI |
| `atwaker memory retire` | 可能写入 / 外部动作 | 管理员 | CLI |
| `atwaker task progress` | 可能写入 / 外部动作 | 管理员 | CLI |
| `automation create schedule` | 可能写入 / 外部动作 | 管理员 | CLI |
| `automation create api` | 可能写入 / 外部动作 | 管理员 | CLI |
| `automation create from-file` | 可能写入 / 外部动作 | 管理员 | CLI |
| `automation inspect run` | 读取 | 管理员 | CLI |
| `memory lifecycle inspect` | 读取 | 所属 Waker 的维护者、观察者 | CLI |
| `teams group get` | 读取 | 管理员 | CLI |
| `teams conversation summary` | 读取 | 管理员 | CLI |
| `teams conversation messages` | 读取 | 管理员 | CLI |
| `teams conversation missions` | 读取 | 管理员 | CLI |
| `teams conversation activities` | 读取 | 管理员 | CLI |
| `workflow customize start` | 可能写入 / 外部动作 | 管理员 | CLI |
| `workflow customize list` | 读取 | 管理员 | CLI |
| `workflow customize get` | 读取 | 管理员 | CLI |
| `models preference get` | 读取 | 管理员 | CLI |
| `models preference set` | 可能写入 / 外部动作 | 管理员 | CLI |
| `connector computer-use status` | 读取 | 管理员 | CLI |
| `connector computer-use clear` | 可能写入 / 外部动作 | 管理员 | CLI |
| `knowledge notebook list` | 读取 | 管理员 | CLI |
| `knowledge notebook shared` | 读取 | 管理员 | CLI |
| `knowledge notebook get` | 读取 | 管理员 | CLI |
| `knowledge notebook create` | 可能写入 / 外部动作 | 管理员 | CLI |
| `knowledge notebook delete` | 可能写入 / 外部动作 | 管理员 | CLI |
| `knowledge source list` | 读取 | 管理员 | CLI |
| `knowledge source get` | 读取 | 管理员 | CLI |
| `knowledge source add` | 可能写入 / 外部动作 | 管理员 | CLI |
| `knowledge source delete` | 可能写入 / 外部动作 | 管理员 | CLI |
| `remote employee list` | 读取 | 管理员 | CLI |
| `remote employee get` | 读取 | 管理员 | CLI |
| `remote employee config` | 读取 | 管理员 | CLI |
| `remote employee config-summary` | 读取 | 管理员 | CLI |
| `remote employee capabilities-refresh` | 可能写入 / 外部动作 | 管理员 | CLI |
| `remote employee refresh` | 可能写入 / 外部动作 | 管理员 | CLI |
| `auth github status` | 读取 | 管理员 | CLI |
| `auth github cli-status` | 读取 | 管理员 | CLI |
| `diagnostics runtime-env list` | 读取 | 管理员 | CLI |
| `diagnostics runtime-env hide` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel dingtalk group search` | 读取 | 管理员 | CLI |
| `channel dingtalk user search` | 读取 | 管理员 | CLI |
| `channel dingtalk user resolve` | 读取 | 管理员 | CLI |
| `channel dingtalk scene-group upgrade` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel dingtalk scene-group bind` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel dingtalk scene-group downgrade` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel feishu account search` | 读取 | 管理员 | CLI |
| `channel feishu account resolve` | 读取 | 管理员 | CLI |
| `channel feishu conversation search` | 读取 | 管理员 | CLI |
| `channel wecom-bot provider install` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel wecom-bot provider authorize` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel wecom-bot provider install-and-authorize` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel wecom-bot conversation search` | 读取 | 管理员 | CLI |
| `channel wecom-bot account search` | 读取 | 管理员 | CLI |
| `channel wecom-bot account resolve` | 读取 | 管理员 | CLI |
| `channel dingtalk-user self-direct-receipts list` | 读取 | 管理员 | CLI |
| `channel dingtalk-user self-direct-receipts resolve` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel dingtalk-user dws status` | 读取 | 管理员 | CLI |
| `channel dingtalk-user dws bootstrap` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel dingtalk-user dws bootstrap-status` | 读取 | 管理员 | CLI |
| `channel dingtalk-user dws logout` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel dingtalk-user dws uninstall` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel dingtalk-user conversation search` | 读取 | 管理员 | CLI |
| `channel dingtalk-user account search` | 读取 | 管理员 | CLI |
| `channel dingtalk-user account resolve` | 读取 | 管理员 | CLI |
| `channel dingtalk-user shared-identity get` | 读取 | 管理员 | CLI |
| `channel dingtalk-user shared-identity set` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel dingtalk-user ignore-conversation add` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel dingtalk-user ignore-conversation remove` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel pairing targets set` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel pairing target list` | 读取 | 管理员 | CLI |
| `channel pairing target set-model` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel pairing config set` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel pairing conflict get` | 读取 | 管理员 | CLI |
| `channel pairing conflict check` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel pairing at-waker set` | 可能写入 / 外部动作 | 管理员 | CLI |
| `atwaker im messages search` | 读取 | 管理员 | CLI |
| `atwaker im messages get` | 读取 | 管理员 | CLI |
| `atwaker im messages around` | 读取 | 管理员 | CLI |
| `atwaker im messages range` | 读取 | 管理员 | CLI |
| `atwaker im files list` | 读取 | 管理员 | CLI |
| `atwaker im files download` | 可能写入 / 外部动作 | 管理员 | CLI |
| `atwaker task reporter claim` | 可能写入 / 外部动作 | 管理员 | CLI |
| `atwaker task result submit` | 可能写入 / 外部动作 | 管理员 | CLI |
| `atwaker task result status` | 读取 | 管理员 | CLI |
| `channel feishu account auth status` | 读取 | 管理员 | CLI |
| `channel feishu account auth start` | 可能写入 / 外部动作 | 管理员 | CLI |
| `channel feishu account auth connect` | 可能写入 / 外部动作 | 管理员 | CLI |
| `waker update coreCapabilities` | 可能写入 / 外部动作 | 所属 Waker 的维护者 | CLI |
| `waker update workStyles` | 可能写入 / 外部动作 | 所属 Waker 的维护者 | CLI |
| `waker update deliveryCommitments` | 可能写入 / 外部动作 | 所属 Waker 的维护者 | CLI |
