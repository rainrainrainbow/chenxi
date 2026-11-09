# 安全策略

## 信任模型

晨曦沿用「**单可信操作者**」模型：一台 S905M 归一个人使用，Gateway 绑定
`127.0.0.1:7788`，不是多租户隔离边界。会话可见性、Agent 身份都是功能划分，
不是安全边界。

- **不要**把 7788 端口、UDS socket 或 `chenxi-hostd` 直接暴露到公网；
  远程访问请走 SSH 端口转发或 Tailscale。
- 入站文本（含工具输出）一律视为**不可信数据**，不视为指令。
- 密钥只从环境变量 `CHENXI_API_KEY` 读取，不写进 `config.json`、不写进事件日志。

## 四层权限

1. **进程沙箱**：systemd `NoNewPrivileges` / `ProtectSystem=strict` /
   `CapabilityBoundingSet=` / 独立低权限用户。
2. **Host 授权**：`deploy/hostd.json` 的 capability 白名单由 hostd 自己决定，
   Go 侧无法扩权。
3. **AI 档位**：`readonly` / `workspace` / `full`；`full` 只放宽 AI 限制，
   不提升操作系统权限。
4. **用户审批**：`ask` 生成一次性 token，带外 `/v1/approve` 确认后才执行；
   授权结果**不缓存**，每次执行前重新校验。

内部 sandbox 字段与 UI 不得被描述成真正的 OS 级隔离。

## 报告漏洞

请通过 GitHub 私有安全通告（Security Advisories）提交，不要开公开 issue。
有效的报告请包含：可复现步骤、受影响组件与 commit SHA、跨过了上述哪一层边界、
以及修复建议。

以下通常**不**算漏洞：仅有 prompt injection 而无策略/审批/沙箱绕过；
可信操作者主动启用的本地功能；多用户共享一个 Gateway 却期待按用户隔离；
把已文档化的部署风险（公网暴露）当作漏洞。

## 已知边界

本项目为静态源码交付，**未在真机烧录、未压测、未做第三方安全审计**。
内存数字是设计预算，不是实测值。生产使用前请自行验证。
