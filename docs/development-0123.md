# 0.12.3：BYOK 运行开关、遥测控制与诊断

交付日期：2026-10-08。范围：四个缺口增量 + 一处既有缺陷修复 + 两个升级阻断修复；生产 Panel 升级与 daemon 首次受管迁管。

## 缺口增量

1. **BYOK 运行开关**：`QODER_SDK_CUSTOM_BASE_URL_BYOK` 纳入 runtime 策略——持久化（缺省开启，旧 `runtime-policy.json` 兼容）、下次启动生效值、受管 daemon 进程实况（`read_daemon_environ`）与一致性提示；进程未携带开关按官方默认（关闭）判定，独立 `sdkByokPending`，不混入既有 `pendingRestart` 聚合。未受管 daemon 显示“未知”，不推断。
2. **/api/telemetry**：确认门路由，经 daemon CLI `config set telemetry` 写入官方配置；安全页开关按钮。
3. **Provider 探测 403 诊断**：三个实测根因（开关未开/未重启、Provider 键与目录同名冲突含 catalog 磁盘缓存、endpoint/vpc 残留）。
4. **出网白名单静态清单**：安全页展示 `config/whitelist.json`。

## 缺陷与阻断修复

- 遥测显示此前读 `config/settings.json`（无该键，永远“未确认”），daemon 实际写 `config/config.json`——改对读取源。
- 受管启动兼容：`PANEL_ENV` 白名单收编新开关；`restart-panel.sh` 导出该开关且期望版本随 panel VERSION 锚定（测试同步锚定，防止版本升级再漏）。

## 验收

- 套件 247 项：macOS 245 通过 / 2 Linux-only 跳过；Linux 服务器 247/247。
- 升级流程：受管 stop（旧版本完整时）→ 私有目录备份（运行文件、panel.env、令牌、SQLite、process-state）→ 替换 → 新版受管启动 → health 0.12.3。升级中断安全点：stop 失败即中止（面板仍在运行）。
- daemon 首次迁管：旧进程身份五项存档（stat/cmdline/environ/exe/socket fd）→ 验证唯一监听者后 SIGTERM 优雅停止 → 端口释放确认 → `qw-ctl.sh cn start` 受管启动（direct 模式，BYOK 开关入 env）→ 登录态与模型注册表核验。
- 端到端：面板会话发消息 → 自有 Provider 实调 → 助手回复返回（生产 Panel + 受管 daemon）。
- 运行开关面板实况：`observed [{hotDeploy: false, embeddingDisabled: true, sdkByok: true}]`，与保存策略一致。

## 边界

- daemon 受管迁管后，自动重启入口按设计仍要求维护门与空闲核对；未做 daemon 之外的网关迁管。
- 遥测开关只控制官方遥测配置，不代表全部上行阻断（页面文案已注明）。
- Embedding 官方无 BYOK 面（1.1.6 硬编码），面板只提供“禁用官方 Embedding”开关。
