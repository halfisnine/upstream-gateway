# 本地开发接手入口（2026-11-16）

本仓库为 `upstream-gateway`，工作目录已从运维仓库分离，当前正式开发位置为 `/opt/dev-workspace/upstream-gateway`。
先读 `.dev/README.md`、`.dev/baseline.json` 和现有构建发布说明。使用本仓库 `.dev/run`，
缓存、产物、开发配置按项目隔离，不依赖旧 `/opt/snewapi/source` 或 `/opt/snewapi/gateway`。
当前基于已保存国内提交，Instabox 未推送修改未导入；不要声称已恢复那次前端工作。
主分支为 `main`；新增功能使用 `codex/` 分支。开发、镜像发布、线上部署分别按任务范围执行。
不要编辑相邻项目，不要停止生产服务。读取下列原有规范及对应子目录规范后再修改业务。

# Development rules

- This is an independent Go gateway, not a fork of New API.
- Go is pinned to 1.26.10; use the digest-pinned Dockerfile. Do not follow latest.
- Current scope is a publishing bootstrap, not a working model proxy. Keep this explicit.
- Do not implement automatic model retries, continuations, or fallback requests.
- Never commit credentials, production data, local tool authentication, or upstream keys.
- Run `go test -race ./...`, `go vet ./...`, and the container smoke check for relevant changes.
- PRs and ordinary main commits build only. Tags and explicit manual publication may push images.
- Publication must not deploy to a production server. Record the image digest for deployment.
- Health/liveness does not imply readiness to accept model requests.
