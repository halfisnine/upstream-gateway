> 接手快照：2026-11-16，从运维工作区 docs/ 复制。历史相对链接可能指向原运维目录；开发工具以本项目 .dev/README.md 为准。

# 独立网关 GitHub Actions 构建与镜像发布

日期：2026-11-15（Asia/Shanghai）。

## 仓库与职责

| 组件 | 位置与职责 |
| --- | --- |
| GitHub | https://github.com/halfisnine/upstream-gateway ，公开的独立源码仓库 |
| Docker Hub | https://hub.docker.com/r/vidbin/upstream-gateway ，公开镜像仓库 |
| 当前服务器源码 | `/opt/snewapi/gateway`，独立 Git 仓库 |
| Instabox 源码 | `/data/workspace/snewapi/gateway` |
| GitHub Actions | 测试、构建 Linux amd64 镜像、按条件发布 |
| Vultr | 后续拉取已验收镜像；本次未部署 |

完整源代码与操作说明见 [网关 README](../gateway/README.md)，工作流见
[build.yml](../gateway/.github/workflows/build.yml)。不将整个运维仓库、生产配置、
服务器地址或开发工具登录信息上传到公开源码仓库。

## 发布验证骨架

当前代码仅实现版本信息、管理健康检查和正常退出，尚未实现业务代理。
管理地址默认 `127.0.0.1:9090`，`GATEWAY_ADMIN_LISTEN` 可覆盖。
`GET /healthz` 为 200，`GET /readyz` 为 503，其余模型路径返回 404。
不能用该镜像替换线上转发服务。

Go 编译器固定 1.26.10，基础镜像使用已锁定摘要；运行镜像为 scratch + CA
根证书，以 UID/GID 65532 运行。当前只有标准库，无外部 Go 依赖。
测试包含拒绝模型请求和未就绪语义，并在实际容器验证只读文件系统、健康接口、
SIGTERM 正常退出。竞争检测使用 CGO，正式二进制使用 CGO_ENABLED=0。

## 构建与发布触发

- main 提交、PR：测试和构建，不使用 Docker Hub 写入凭证，不发布。
- 版本标签 `v*`：格式校验、测试、构建与启动验证通过后发布。
- Actions 手动运行：默认不发布；仅 main 分支且 publish 勾选时发布。
- 发布生成版本标签和 `sha-完整提交` 标签，任务摘要记录仓库清单 digest。
- 不生成 latest，不连接生产主机，不执行生产部署。
- Docker Hub 标签本身可变，重新运行发布可能覆盖标签；正式部署锁定完整摘要。

Secrets 已由用户在 GitHub 配置：`DOCKERHUB_USERNAME`、`DOCKERHUB_TOKEN`。
仅核对其名称存在，不读取令牌值。Actions 内临时 Docker 凭证目录在步骤结束后清理。
GitHub Actions 自带只读 GITHUB_TOKEN 用于检出，不持久保存到 Git 配置。

## 首次验证

- Instabox：格式检查、go vet、go test -race、镜像构建和容器启动/退出验证通过。
- 初始化源码提交：`1b4c195425debdc75abb39b692457820b68fdce3`。
- main 首次任务：https://github.com/halfisnine/upstream-gateway/actions/runs/43716668784 。
- main 首次任务成功，构建及镜像验证通过，发布步骤按预期跳过。
- 标签发布任务成功：https://github.com/halfisnine/upstream-gateway/actions/runs/43716875433 。
  作业耗时约 75 秒，其中发布步骤约 10 秒；这只是该小型镜像的一次实测。
- 发布版本：`vidbin/upstream-gateway:v0.0.0-bootstrap.1`。
- 仓库清单摘要：`sha256:68661a4b77728cf365a40f7e07ae769a7ab9ee0e29d2c6bd44c81cefba529ce3`。
- Instabox 已从 Docker Hub 按上述摘要拉取，并重新完成版本、健康状态、未就绪、
  模型路径拒绝、正常退出检查；镜像标签中的源码提交与初始提交一致。
- 已发布镜像为 Linux amd64，运行用户 `65532:65532`。尚未在 Vultr 拉取或运行。
- 首次发布提示 checkout v4 的 Node.js 20 已弃用；main 后续改为锁定
  checkout v5 的 Node.js 24 实现，保留已发布版本标签，未覆盖镜像。
- checkout 兼容性修正提交：`1ef15a6bc6c6eb326d72daa6a8329681a9bfef87`；云端检查成功：
  https://github.com/halfisnine/upstream-gateway/actions/runs/43717117595 。

## 后续开发

在独立仓库内迭代真正的网关功能，遵循已批准的详细设计：固定路由、内部认证、
上游凭证注入、非成功 HTTP 错误隔离、成功 JSON/SSE 有界转发。不要将当前验证
骨架的健康检查通过视为模型业务验收。

先让 main 检查通过，再创建新的不可复用版本标签。检查 Actions 发布任务的
摘要和日志，将通过测试的 `vidbin/upstream-gateway@sha256:...` 写入目标部署配置。
生产发布与回滚应单独执行。
