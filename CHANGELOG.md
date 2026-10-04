# Changelog

## 0.12.0 — 2026-10-04

首个独立公开快照，包含此前 0.7–0.11 的已验证面板能力，以及 0.12 的管理面加固。

- Provider 配置增加完整字节 revision、协作锁、最终替换前复核和单份恢复点；复杂配置保持只读，主动探测先确认具体凭据发送目标。
- Waker / Provider 删除采用依赖预览、fresh 重扫、一次性计划与幂等操作；pending / unknown 保持目标锁，畸形或不可用依赖源阻断删除。
- Maintenance admission 与 drain 要求请求排空和 daemon activity 完整空闲；不冒充全局工作锁。
- daemon frontend session 使用官方 CLI 合约，复用会话、有界响应、只读重建一次，写请求不自动重放。
- Linux 进程管理增加精确身份、socket ownership 与 pidfd；网关增加严格代际、operation journal 和保守 GC。旧非受管进程不会自动接管。
- 修复只读用户页面加载、缓存失效竞态、JSON null 状态处理、偏好读取失败显示和 pidfd 退出过渡态。
- 增加通用部署与升级文档，说明旧进程迁移、停止后替换脚本、备份与回滚顺序。

验收：macOS Python 189 通过 / 1 Linux-only 跳过，隔离 Linux Python 190/190，JavaScript 9/9；干净快照卫生检查通过。服务器仅升级 Panel，17/17 运行文件哈希匹配，管理员/只读权限与官方 daemon 读取通过；daemon、旧网关、配置和令牌保持不变。

限制：不是官方完整控制台、整机零上行、完全锁版或受保护业务免登录。旧 daemon / 网关首次受管迁移、0.12 真实 BYOK 聊天、IM / 插件写入 / 删除及官方全页视觉对照仍未验收。详见 [验收矩阵](docs/acceptance.md)。

许可：PolyForm Noncommercial 1.0.0；非商业用途授权，商用需另行取得授权。项目与 Qoder / QoderWake 官方无隶属关系。
