# upstream-gateway

独立 Go 上游网关，计划放置在 New API 与模型上游之间。

**当前是构建发布验证工程，尚未实现上游转发、认证、路由或错误隔离。
不能接入真实模型流量，不是生产可用版本。**

## 当前能力

- `--version` 输出版本和源码提交。
- 管理监听默认 `127.0.0.1:9090`，可用 `GATEWAY_ADMIN_LISTEN` 修改。
- `GET /healthz` 返回 200，仅表示进程存活。
- `GET /readyz` 返回 503，明确表示网关业务尚未实现。
- 其他路径返回 404，不向任何上游发送请求。
- SIGTERM/SIGINT 触发有时限的正常关闭。

计划中的正式网关将支持按路由选择上游、内部认证、上游凭证注入、非成功
HTTP 错误隔离及成功 JSON/SSE 转发。不会默认进行重试或自动续写。
HTTP 200 内嵌错误及 SSE 错误的语义处理是后续扩展，不能据此承诺完全隔离。

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
| Actions 手动运行 | 是 | 仅 main 分支且勾选 publish 时 |

仓库需配置 `DOCKERHUB_USERNAME`、`DOCKERHUB_TOKEN` 两个 Actions Secrets。
令牌仅在发布步骤注入，不写入源码和镜像。Actions 不部署或登录生产服务器。

发布标签示例 `v0.0.0-bootstrap.1`，镜像具有同名标签和 `sha-完整提交` 标签。
手动发布使用 `dev-完整提交`，不生成 latest。版本标签发布后不要移动或复用；
Docker Hub 标签本身仍可变，实际部署必须使用成功任务摘要中的 `image@sha256:...`。
重新运行发布任务可能重写同名标签，应以记录的摘要为准。

Actions 只使用完整提交 SHA 锁定的 checkout Action；编译器镜像同样按摘要锁定。
GitHub 托管运行器和预装 Docker 由平台维护，并非全部系统组件都被冻结。

先等待 main 检查通过，再发布版本标签。构建验证版只用于检查发布链路，
业务功能和真实模型验收完成之前，不更新线上 New API 渠道。
