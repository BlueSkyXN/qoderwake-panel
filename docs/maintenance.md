# 维护、快照与 PR 约定

适用于 Panel **0.12.2**，更新日期 **2026-10-06**。公开源码发布与生产 Panel 单独升级均已完成；旧 daemon/网关未首次迁管，历史标签和附件保持不变。本项目公开源码采用 [PolyForm Noncommercial 1.0.0](../LICENSE)；非商业用途按许可使用，商业使用需另行授权。项目不是 Qoder / QoderWake 官方产品。

## 两个仓库的职责

- 私有研究仓是日常开发与实验场，保存部署记录和未脱敏材料。
- [公开仓](https://github.com/BlueSkyXN/qoderwake-panel) 是独立的标准化源码快照；维护者手工挑选已验证内容、脱敏并审查后提交。不搬运私有 Git 历史，不自动同步，也不要求两仓逐文件相同。
- 零补丁与补丁工具都可以进入公开快照；是否公开取决于验证与脱敏，不按技术路线划分公私版本。
- 服务器部署是第三个独立环节。源码已提交、PR 已合并或 Release 已发布，都不能证明运行进程已更新或真实业务已验收。

## 0.12.4 当前发布基线

- [`v0.12.4`](https://github.com/BlueSkyXN/qoderwake-panel/tree/v0.12.4) 固定对应 `6a664df`；[Release](https://github.com/BlueSkyXN/qoderwake-panel/releases/tag/v0.12.4) 提供 tar.gz、zip 与 SHA256SUMS，下载后哈希核对一致，tar.gz 73 个文件。
- 启动、重启和页面加载不再计算受管文件哈希；内容哈希只在增强管理页按需、后台执行。生产面板已切换到 0.12.4。

## 0.12.3 历史基线

- [`v0.12.3`](https://github.com/BlueSkyXN/qoderwake-panel/tree/v0.12.3) 固定对应 `756d4cae45a8d1b5d03ba39212303cf41a5462a7`；[Release](https://github.com/BlueSkyXN/qoderwake-panel/releases/tag/v0.12.3) 提供 tar.gz、zip 与 SHA256SUMS，下载核对哈希一致、tar.gz 72 文件。
- 双平台套件：macOS 245 通过 / 2 Linux-only 跳过；Linux 247/247。生产 Panel 受管升级至 0.12.3；daemon 已完成首次受管迁管（BYOK 开关入 env、实况三态一致）；面板会话经自有 Provider 实调返回回复。
- daemon 自动重启仍要求维护门与空闲核对；旧网关迁管仍未进行。

## 0.12.2 历史基线

- [`v0.12.2`](https://github.com/BlueSkyXN/qoderwake-panel/tree/v0.12.2) 固定对应 `644a4208c300678c676281fcc3b1a88012ac228b`；[Release](https://github.com/BlueSkyXN/qoderwake-panel/releases/tag/v0.12.2) 提供 tar.gz、zip 与 SHA256SUMS，下载后两包各 71/71 文件匹配。
- 两仓修复 PR 已合并；公开提交在 Linux 隔离复跑 242/242 和真实 daemon 只读连接通过。生产 Panel 单独升级至 0.12.2，19/19 文件、精确身份、权限、数据库和维护状态通过。
- daemon/旧网关未重启或首次迁管。面板尾读与用量游标已生效，旧网关日志轮转未生效；用量历史仍按请求限量回填。
- 49 文件私有备份权限和一致性通过；专用回滚在隔离实例完成 0.12.2 → 0.12.1 演练，生产未执行回滚。交付事实补到 main，不移动标签或覆盖附件。

## 0.12.1 历史发布基线

- [`v0.12.1`](https://github.com/BlueSkyXN/qoderwake-panel/tree/v0.12.1) 固定对应 `360bd9110e87d63d495dc9ccd0bbdca36d12a77b`；[Release](https://github.com/BlueSkyXN/qoderwake-panel/releases/tag/v0.12.1) 提供 tar.gz、zip 与 SHA256SUMS。
- 两个附件重新下载后各 66/66 文件与标签一致；生产 Panel 单独升级到 0.12.1，运行文件 17/17 一致，daemon/旧网关不变。
- 标签内文档是发布前修复/验证记录；部署验收补到 main，不移动已有标签，不覆盖附件。完整范围见 [验收矩阵](acceptance.md)。

## 0.12.0 历史发布基线

| 对象 | 记录 |
|---|---|
| 源码标签 | [`v0.12.0`](https://github.com/BlueSkyXN/qoderwake-panel/tree/v0.12.0) |
| 标签对应的快照提交 | `e4583bbe1c5de217f1db08a7eaba89037d31bfa1` |
| 发布入口 | [QoderWake Panel 0.12.0](https://github.com/BlueSkyXN/qoderwake-panel/releases/tag/v0.12.0) |
| 自带发布附件 | `qoderwake-panel-0.12.0.tar.gz`、`qoderwake-panel-0.12.0.zip`、`SHA256SUMS` |
| 发布时下载复核 | 两个源码附件校验和通过，解包后的 63/63 文件与快照提交一致 |

发布后的文档修订可以继续进入 `main`，因此 `main` 不必与版本标签或源码附件逐字节相同。既有标签、附件与校验和保持原样；文档修订不冒充新的运行版本，也不覆盖已发布附件。后续改变运行代码时，应重新测试、选定新版本并生成独立发布记录。

本次公开快照有意排除四个旧登录/向导实验脚本：`cli-login-wait.py`、`login-pty-cn.py`、`login-pty.py`、`wizard-frame.py`。公开 `check_release.py` 使用通用凭据和私有路径规则，不复制私有部署标识。

## 日常修改与 PR

1. 明确修改范围，在主题分支修改源码、测试和对应文档。已有其他项目的未提交改动必须保留，不使用整仓 `git add -A` 混入本次提交。
2. 从最新远端引用核对分支差异。PR 应说明实现内容、实际执行的测试、平台跳过项，以及未进行的部署或真实业务验收。
3. 按文件路径审查和暂存。文档链接、版本、命令和完成边界必须与源码及证据一致；历史验收记录保留日期，不改写成当前状态。
4. 推送主题分支后创建指向该仓 `main` 的 PR，检查 review、checks 和 mergeability。没有配置自动检查时，应明确记录本地验证，不能把“没有 checks”说成“CI 全绿”。
5. 有冲突时先读取双方修改并保留各自意图；修复后重跑受影响的测试。不得以覆盖一方文件、强推 `main` 或绕过权限来消除冲突。
6. 合并后核对远端 `main` 包含提交、PR 为 merged。提交、推送与合并分别报告；PR 合并不自动触发服务器升级或发布新标签。

## 发布前门禁

在独立、干净的公开候选目录执行以下命令：

```bash
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests -v
node --check panel/static/panel.js
node --check panel/static/management.js
node --check panel/static/advanced.js
node --test tests/test_*.mjs
bash scripts/check-release.sh
git diff --check
```

Python 是运行时依赖，Node 仅用于开发期语法和 JavaScript 回归测试。运维 shell 变更另跑 `bash -n`。涉及 Linux 精确进程或网关切换时，还需在临时 HOME、目录和回环端口做 Linux 隔离验证；macOS 跳过真进程测试不代表该项通过。

`check-release.sh` 只检查，不复制、不提交、不推送。门禁通过不是安全认证；维护者仍须检查新增文件、差异及包内文件清单。令牌、真实配置、数据库、日志、私钥、恢复点、运行 state、生成物和测试缓存不得进入公开候选。私有目录中的既有缓存无需为发布而删除，候选应通过手工选择隔离。

## 运行态与恢复边界

2026-10-06 部署与最终复验只证明 Panel 单独升级至 0.12.2：19/19 运行文件哈希匹配，精确受管身份、端口、健康、权限与数据库检查通过。既有 daemon、旧网关、Provider 配置、官方认证、访问令牌和公网/HTTP 绑定保持不变。本次核验 49 个备份文件及权限，专用回滚入口已通过隔离演练，但未执行生产回滚。后续状态仍需实时核对，不能永久沿用此快照。

- Panel 已建立受管 state；生产旧 daemon 与网关尚未首次迁管，新启动器拒绝未知监听是预期保护。
- 升级 Panel 必须先用仍匹配当前身份的控制器停止，再备份和替换代码。不要在停止前覆盖身份记录引用的脚本。
- 保留旧代码、启动环境、实例配置、令牌及 SQLite 一致快照，敏感备份只留私有目录。回滚材料准备和生产回滚演练是两件事；0.12 记录只有前者完成。
- 既有公网/HTTP 部署不会被文档修订自动改为回环、隧道或 HTTPS。HTTP 不提供传输加密；是否调整绑定、证书或令牌属于独立部署决策。
- 生产 daemon/网关首次迁管、systemd、真实 BYOK 聊天、IM、插件写入及资源删除仍需独立授权和验收。不得随 PR 合并顺带执行。

升级和回滚的具体顺序见 [部署文档](deployment.md)，当前功能与未完成目标见 [验收矩阵](acceptance.md)。
