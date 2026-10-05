# Changelog

## 0.12.1 — 2026-10-05

- 保留两项官方上行开关的显式值；已有关闭项在重启时缺失或变为开启会拒绝，不再静默丢弃。
- Group 详情核对目标 ID；Waker/Provider 两类删除统一验证成员和负责人，畸形依赖阻断 fresh scan。
- Panel/daemon 与网关在绑定 pidfd 前后均检查 socket ownership；unknown/conflict 不发信号，已验证 unbound 可清理失败启动。
- 原生 TLS 的受管健康探测支持指定 CA 与服务器证书名称，连接仍限制回环，保持证书验证。
- 控制器绑定保存实例的 HOME/端口/启动路径/模式；状态与停止不混用另一实例的健康地址。
- daemon 状态及运行开关观测复用完整进程身份；读取环境前后验证，PID 复用或不可确认时不报告已生效。
- 发布检查拒绝目录链接、断链和非常规文件；现有空对象/null 等畸形 Provider 在服务端统一只读。

验证及限制见 [0.12.1 修复记录](docs/development-0121.md)，发布与部署结果见 [验收矩阵](docs/acceptance.md)。修复阶段没有部署或创建标签；0.12.0 标签和附件保持原样。

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
