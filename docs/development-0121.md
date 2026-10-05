# 0.12.1 全面复查修复

日期：2026-10-05。源码版本 **0.12.1**。本文件记录修复与隔离测试阶段；该阶段未部署、未创建 Release，生产与已发布标签仍为 **0.12.0**。后续发布与部署分别记录在 [验收矩阵](acceptance.md)。此记录更正 0.12.0 对若干保护分支的过度概括，不重写历史测试结果。

## 修复清单

| 复查发现 | 0.12.1 处理 | 回归证据 |
|---|---|---|
| 上行禁用变量被最小环境丢弃 | Panel/daemon 白名单保留两项官方上行开关，验证合法值；state 中已关闭的项不允许在重启时静默消失或开启 | 双层 child environment、无代理/secret 继承、非法值、停止前拒绝丢失关闭项 |
| 畸形 Group 依赖放行删除 | Provider 与 Waker 都验证成员/负责人身份；Group 详情 ID 必须与请求一致 | 缺失/空值/冲突身份转 unknown；HTTP fresh scan 在依赖变坏后拒绝且原 Provider 字节不变 |
| unknown socket ownership 仍发送 signal | 两个控制器在打开 pidfd 前及发信号前检查端口归属 | unknown/other 不发信号，打开 pidfd 后变坏也拒绝；确认 unbound 的精确进程可清理 |
| TLS Panel 无法通过受管 health | 回环 HTTPS 探测、可配置 CA 与证书名称，启动器按 TLS 配置选择协议 | 可信证书成功、不可信 CA/错误名称失败；Linux 真 Panel TLS start/status/stop |
| state 与控制器入参混用 | 比较 HOME、端口、profile、启动路径、命令、mode/endpoint、Panel root 和传输协议 | status/stop/start 对实例参数不一致均拒绝，不访问错误 health、不发送 signal |
| PID 复用导致开关“已生效”误报 | Panel 复用 process-control 的完整 state/身份/socket 核验；读环境前后复核身份 | fake `/proc` 的 PID 复用、HOME 改变、socket 不归属、读取期间身份变化均不报告 applied |
| 发布门禁漏目录 symlink/断链 | 文件类型判断前检查链接；拒绝链接和非常规文件 | 文件链接、目录链接、断链、FIFO 均被门禁拒绝 |
| 空对象 Provider 绕过只读保护 | 以名称是否存在而非旧值真假判定编辑现有项 | HTTP 写入 `{}` / null / false / list 均稳定拒绝，原文件及恢复点不变 |

环境只保留已声明字段，不恢复任意父环境继承。两个上行变量未配置时仍沿用官方默认值；本轮没有替用户改变生产策略，也不能将开关保留解释成整机零上行。

停止保护允许 `owned` 和可验证 `unbound`，后者用于清理已经失去监听的精确进程和启动失败；`unknown` / `other` 不可降级。信号仍只通过 pidfd，退出后继续核对监听已释放。

## 兼容与部署

- process state schema 保持 1，不修改既有用户 state。旧非受管 daemon/网关仍拒绝自动接管。
- 停止现有实例必须使用与 state 匹配的旧 HOME、端口、启动路径和模式。改变参数先显式停止，不用新参数静默替换当前实例。
- TLS 健康探测固定连接回环；`QW_HEALTH_SERVER_NAME` 只用于 SNI/证书名称，`QW_HEALTH_CA_FILE` 只为该探测提供信任，不改系统证书库。详见 [部署文档](deployment.md)。
- Panel 的 daemon 观测现在依赖 `ops/process-control.py`；标准目录保留 `panel/` + `ops/`，已有扁平部署支持控制器与 Panel 同目录。
- Panel 发起 daemon status/start 时传入保存的 mode、endpoint、port，避免由默认 direct/19830 参数与真实实例混用。

## 验证与完成边界

本轮新增 `tests/test_safety_review.py` 及发布/启动器回归，使用合成 Provider、临时 HOME、fake `/proc`、随机回环端口和临时证书；没有真实模型调用或用户资源删除。

Linux 最终隔离全集 **212/212 通过**，包含既有真实网关切换/回滚/启动失败恢复，以及新增真实 TLS Panel start/status/stop。结束后没有该临时目录的残留测试进程，临时目录已清理。TLS 信任仅在测试客户端上下文，不写系统信任。

macOS 私有与公开工作副本各 **212 项：210 通过、2 Linux-only 跳过**；JavaScript 各 **9/9**，Python/JS/shell 语法、33 个文档相对文件链接及两仓干净候选卫生检查通过。两项跳过分别是真实网关切换和真实 TLS Panel 启停，均在 Linux 最终全集执行通过。最终运行源码与测试指纹匹配 Linux 输入清单，之后只更新本段验证记录。

修复阶段不代表生产已修复：该阶段生产 Panel 仍为 0.12.0；没有部署、停服、迁管、令牌轮换、网络修改、PR 合并或新标签。后续已按独立步骤完成两仓修复 PR 合并、v0.12.1 源码发布、66/66 文件下载核验及生产 Panel 单独升级（17/17 运行文件一致）；结果见 [验收矩阵](acceptance.md)。这不扩展为底层首次迁管或真实业务完成。真实 BYOK/IM/插件/审批/生产删除、官方全页视觉对照及生产回滚仍属独立验收。
