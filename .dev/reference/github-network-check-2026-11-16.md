> 接手快照：2026-11-16，从运维工作区 docs/ 复制。历史相对链接可能指向原运维目录；开发工具以本项目 .dev/README.md 为准。

# 国内服务器 GitHub 连接核查

日期：2026-11-16。测试位置 `/opt/snewapi` 所在国内服务器，目标仓库
`halfisnine/silvermoon-ai`。本次不修改远程分支，不上传代码，不更改 Git remote 或认证配置。

| 检查 | 结果 |
| --- | --- |
| GitHub 仓库 API，2 次 | HTTP 200；约 0.68 / 0.93 秒 |
| HTTPS Git receive-pack 引用发现，2 次 | 均在 18 秒超时；TLS 握手已完成，但未收到 HTTP 响应 |
| HTTPS Git upload-pack 引用发现，2 次 | 均连接超时，约 8 秒 |
| `git ls-remote`，只读远程分支查询 | 22 秒超时 |
| `git push --dry-run`，不会更新远程 | 22 秒超时 |
| github.com:22 TCP + SSH banner | 约 0.55 秒收到 SSH banner |
| ssh.github.com:443 TCP + SSH banner | 约 1.02 秒收到 SSH banner |
| ssh.github.com:443 SSH 握手和主机身份核验 | 约 2.12 秒到达用户认证，未提供凭证，预期返回 Permission denied (publickey) |

SSH 主机公钥来自 HTTPS `https://api.github.com/meta`，存入自动删除的临时
known_hosts；启用严格校验，未修改用户 known_hosts。验证时不使用用户私钥或 SSH agent。
Git 测试也禁用了交互认证和 credential helper，仓库未配置 credential helper。
这些结果不代表已完成 GitHub 写权限授权。

结论：当前 HTTPS Git 链路多次超时，不能据此给出实际代码上传带宽；API 可达
不代表 Git 推送可用。SSH 443 已到认证阶段，适合作为下一步候选连接方式。
需配置项目授权后，再做有授权的推送和传输验证，才能评价真实上传速度与稳定性。
本次是短时抽样，不代表长期可用性。不能把小型 API 请求的 bytes/time 当成上传带宽。
