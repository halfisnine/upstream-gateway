# upstream-gateway

独立 Go 上游网关，计划放置在 New API 与模型上游之间。

**当前为本地开发候选：已实现运维页面、数据库完整配置版本、多节点热更新和四协议转发。尚未通过真实 New API 计费、供应商兼容和生产容量验收，不能据此直接接入生产流量。**

## 当前能力

- 中文运维页面：渠道编辑、多协议覆盖、完整版本发布、无凭证字段的历史、恢复版本、节点同步状态。
- PostgreSQL 保存不可变完整快照；AES-256-GCM 加密，主密钥独立提供。
- 节点每 5 秒检查更新，校验后整体切换；新请求使用新配置，旧 SSE 请求继续原配置。
- 单渠道支持 OpenAI Chat / Responses、Anthropic Messages、Gemini 生成与流式接口。
- 按协议校验认证头并转发 New API 携带的 Key、非成功 HTTP 错误隔离、正常 JSON/gzip/SSE 转发及取消。
- 可配置错误规则、预设与样本试匹配；可选检查标准 200 JSON / SSE 错误，规则随完整版本热更新。
- 有界异步拦截日志与管理员查询；原始错误片段加密保存、按需查看，支持截断与凭证脱敏标记。
- 不自动重试、续写、切换上游；未知错误格式和真实供应商兼容仍需验证，不承诺全面信息隔离。
- 未配置数据库时保留 bootstrap 模式：仅管理健康接口，`/readyz` 为 503，无业务监听。

上游 Key 只填写在 New API 中，不在网关渠道中保存。数据面访问来源由内部网络控制，管理页面仍需管理员令牌。旧配置升级见 [凭证转发说明](docs/credentials-passthrough.md)。

错误配置与升级见 [错误规则与日志](docs/error-rules.md)。现行设计见 [设计 v2](docs/design-v2.md)，启动、配置及多节点操作见 [运维说明](docs/operations.md)。旧 `.dev/reference/` 文档为历史背景，冲突处以 v2 为准。

Linux 开发机可启动本项目独立控制台（仅回环监听，不连接生产数据）：

```sh
python3 scripts/dev-console.py start
```

默认管理地址为 `http://127.0.0.1:19090`，登录令牌保存在 `.dev/private/console/admin-token`。完整验收记录见 [交付记录](docs/validation.md)。

## 构建

Go 固定 **1.26.10**，与本项目采用的 New API 开发基线一致。
Dockerfile 固定 Go 镜像摘要，测试阶段执行格式检查、`go vet` 和竞争检测测试，
成功后才构建不依赖 CGO 的二进制。运行镜像使用 scratch、CA 根证书和非 root 用户。
当前仅发布 Linux amd64。

```bash
docker build -t upstream-gateway:dev .
sh scripts/smoke-image.sh upstream-gateway:dev
docker run --rm upstream-gateway:dev --version
```

本机查看管理接口：

```bash
docker run --rm --read-only --cap-drop ALL \
  -e GATEWAY_ADMIN_LISTEN=0.0.0.0:9090 \
  -p 127.0.0.1:9090:9090 upstream-gateway:dev
```

## GitHub Actions 与 Docker Hub

代码仓库：`halfisnine/upstream-gateway`。
镜像仓库：`vidbin/upstream-gateway`。

| 触发方式 | 测试与构建 | 发布镜像 |
| --- | --- | --- |
| Pull request / main 分支提交 | 是 | 否 |
| `v` 开头的版本标签 | 是 | 是，版本格式校验通过后 |
| Actions 手动运行，选择 `codex/` 功能分支 | 是 | 勾选 publish 时发布 `candidate-完整提交号` |
| Actions 手动运行，选择 main | 是 | 勾选 publish 时发布 `dev-完整提交号` |

仓库需配置 `DOCKERHUB_USERNAME`、`DOCKERHUB_TOKEN` 两个 Actions Secrets。
令牌仅在发布步骤注入，不写入源码和镜像。Actions 不部署或登录生产服务器。

发布标签示例 `v0.0.0-bootstrap.1`，镜像具有同名标签和 `sha-完整提交` 标签。
功能分支仅通过手动发布生成 `candidate-完整提交`，不写 `sha-` 或正式版本标签；main 手动发布使用 `dev-完整提交`。均不生成 latest。版本标签发布后不要移动或复用；
Docker Hub 标签本身仍可变，实际部署必须使用成功任务摘要中的 `image@sha256:...`。
重新运行发布任务可能重写同名标签，应以记录的摘要为准。

Actions 只使用完整提交 SHA 锁定的 checkout Action；编译器镜像同样按摘要锁定。
GitHub 托管运行器和预装 Docker 由平台维护，并非全部系统组件都被冻结。

先等待 main 检查通过，再发布版本标签。候选镜像必须先完成实际业务与真实模型验收，之后才按独立部署任务更新线上 New API 渠道。

## 合并前拉取候选镜像验证

功能开发和验证留在 `codex/` 分支，验证通过后再合并 main。Actions → Build and publish → Run workflow，选择功能分支并勾选 `publish`；测试通过后发布到 `vidbin/upstream-gateway:candidate-<完整提交号>`，不需要先合并 main。

从运行摘要复制镜像 digest，使用本仓库的 `compose.validation.yml` 在本地启动独立数据库和网关。完整操作见 [Docker 候选版本验证](docs/docker-validation.md)。拉取、运行镜像不需要编译 Go；镜像发布本身不启动或更新任何服务器。
