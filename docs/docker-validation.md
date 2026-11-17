# Docker 候选镜像验证（合并 main 之前）

开发提交留在 `codex/` 功能分支。GitHub Actions 从选定分支检出代码、测试和构建，推送候选镜像到 Docker Hub；本地 Docker 拉取该镜像运行。Docker Hub 保存构建产物，本地运行无需编译 Go。验证通过后才合并 main，正式发布仍需另行触发，不自动合并或部署。

## 1. 手动发布候选镜像

1. 将待验证代码推送到功能分支，例如 `codex/gateway-console-config-versions`。
2. 打开仓库 **Actions → Build and publish → Run workflow**。
3. 在 **Use workflow from** 选择该功能分支，勾选 `publish`，点击 Run workflow。
4. 等待 Go 检查、PostgreSQL 集成测试、镜像构建和容器启动检查全部通过。
5. 在运行摘要中记录 **Commit**、**Source ref**、镜像标签和 **Image reference**（带 `sha256` 的 digest）。

工作流文件已存在于默认分支，因此可以手动选择功能分支运行它的版本，不必为了验证先合并 main。默认分支界面若仍显示旧的“main-branch”输入说明，以选定功能分支上的工作流规则为准；输入字段仍是同一个 `publish`。

候选标签为 `vidbin/upstream-gateway:candidate-<完整 Git 提交号>`。候选发布不会写 `sha-`、`v*` 或 `latest` 标签。相同提交重跑工作流仍可能重写候选标签，因此一次验收应锁定摘要中的 digest。Docker Hub 凭证使用 GitHub 仓库 Secrets：`DOCKERHUB_USERNAME` 和 `DOCKERHUB_TOKEN`；缺失时发布步骤失败，不能把构建成功当成已经推送成功。

`main` 普通推送和 PR 只构建；功能分支普通推送不触发自动发布。只有手动勾选 publish 才会发布功能分支候选镜像。正式版本标签和 main 手动发布保持原规则。

## 2. 准备独立本地验证环境

需要 Docker Engine / Docker Desktop（Linux 容器）、Docker Compose v2 和 Python 3。当前镜像为 linux/amd64；ARM 主机依赖 Docker 的 amd64 仿真，性能结果不能代表原生运行。下面在该功能分支的仓库根目录执行，命令按 Bash 编写。

```sh
python3 scripts/prepare-docker-validation.py

# 将占位符替换为本次 Actions 摘要中的实际 digest。
export GATEWAY_VALIDATION_IMAGE='vidbin/upstream-gateway@sha256:<实际摘要>'
docker compose -f compose.validation.yml pull
docker compose -f compose.validation.yml up -d
```

PowerShell 可用 `$env:GATEWAY_VALIDATION_IMAGE = 'vidbin/upstream-gateway@sha256:<实际摘要>'` 设置同一变量，其余命令相同。

Compose 没有 `build`，网关和迁移服务都使用指定镜像。数据库镜像也锁定摘要；启动顺序为 PostgreSQL 就绪 → 一次性数据库迁移成功 → 网关启动。首次尚无发布配置时 `/healthz` 为 200、`/readyz` 为 503，需在页面发布首版后才可接收模型请求。

| 用途 | 地址 / 文件 |
| --- | --- |
| 节点 A 控制台 | `http://127.0.0.1:19092` |
| 节点 A 模型入口 | `http://127.0.0.1:18082/r/<渠道标识>` |
| 管理员令牌 | `.dev/private/docker-validation/admin-token`，在本地打开文件复制到登录页 |
| 配置数据库 | Compose 专用 `database` 命名卷，未向宿主机发布数据库端口 |
| 密钥和数据库密码 | `.dev/private/docker-validation/`，已被 Git 忽略 |

这套验证环境使用项目名 `upstream-gateway-validation`，与源码开发节点的 19090/19091、18080/18081、数据库和凭证独立。第一次生成随机秘密，再次执行准备脚本不会替换现有秘密；缺失部分文件或已有默认验证数据卷但丢失秘密时会拒绝生成替代值。保留数据库时也必须保留原有主密钥和数据库密码。

秘密文件通过 Compose 按文件只读挂载。Linux 宿主机秘密目录权限为 0700，文件为 0644，以便容器中的非 root 网关读取；不要把文件移到公开目录。Docker Desktop 的宿主机文件访问权限由所在系统管理。

## 3. 验证多节点和真实接入

```sh
docker compose -f compose.validation.yml --profile multi-node up -d
```

第二节点控制台为 `http://127.0.0.1:19093`，模型入口为 `http://127.0.0.1:18083`。两个节点共用验证数据库与主密钥、使用不同节点标识。

建议按以下顺序验收并记录具体镜像 digest：

1. 登录后新增或修改测试渠道，保存草稿并发布；确认两个节点在约一个轮询周期后报告新版本。
2. 检查历史内容、JSON 折叠、复制/导出和恢复为新版本；确认已开始的流式请求不因发布而断开。
3. 使用测试 New API 配置该网关地址，Key 仍填在 New API。验证实际使用的协议、正常用量和错误行为。
4. 保存通过的提交号、digest、验收范围与结果，再将该功能分支合并到 main；功能代码有后续变更时重新发布候选并验证。

端口仅绑定宿主机回环。在同一台宿主机直接运行的 New API 可用以上地址；New API 若运行在另一容器，它的 `127.0.0.1` 不指向宿主机。可手动将测试 New API 容器接入 `upstream-gateway-validation_default` 网络，并使用 `http://gateway-a:8080/r/<渠道标识>`。不要因此把数据或管理端口开放到公网。实际模型验证会使用你填写的真实上游，应按测试预算操作。

## 4. 更新候选镜像与停止环境

```sh
# 改成新一次候选的 digest，保留同一验证数据库及秘密。
export GATEWAY_VALIDATION_IMAGE='vidbin/upstream-gateway@sha256:<新摘要>'
docker compose -f compose.validation.yml --profile multi-node pull
docker compose -f compose.validation.yml --profile multi-node up -d

docker compose -f compose.validation.yml --profile multi-node ps -a

# 停止验证环境，保留配置数据卷和秘密。
docker compose -f compose.validation.yml --profile multi-node down
```

不要给 `down` 添加 `-v`，除非明确要删除这套验证环境的全部数据库内容。正常停止给予网关 40 秒退出窗口。恢复已知可用候选时设置旧 digest 并重新 up；涉及数据库迁移的跨版本回退需要另行确认 schema 兼容性。

## 5. 发布到正式环境

合并 main 不会自动推送 Docker Hub，也不会部署生产。按现有版本标签或 main 手动发布流程生成正式产物并记录正式 digest。合并后构建的镜像可能与候选 digest 不同，应核对代码差异、执行 CI 和正式产物检查，不能仅凭候选曾通过就把另一个未知产物当作已验收。

## 错误规则版本验证补充

schema 4 镜像先运行迁移再启动节点，原有数据库和秘密继续使用，不需要重新初始化。先更新全部节点镜像，再发布 schema 4 完整配置；旧镜像不能读取新快照。保持原行为时无需开启正文检查；需验证 200 JSON / SSE 错误时在「错误规则」勾选检查开关、添加预设并发布。

验收上游状态与固定文案、两个节点版本、SSE 中途错误和「拦截日志」查询。日志不保存原文，约 1 秒后可见；队列满会丢弃明细，页面按节点报告累计值。新 Compose 同时启用 Docker 日志轮转。具体限制见 [错误规则与日志](error-rules.md)。本地镜像通过测试并不等于已经推送 Docker Hub；沿用手动发布候选流程。
