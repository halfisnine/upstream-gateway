# 本地运维与接入

本版为待真实上游验收的开发候选。设计见 [design-v2.md](design-v2.md)。不默认部署生产或发起真实模型请求。

## 启动开发控制台

Linux 开发机在仓库根目录执行：

```sh
python3 scripts/dev-console.py start
```

脚本使用 `.dev/run` 构建，创建**本项目专用** PostgreSQL 17.11 数据库，固定镜像摘要，无数据库宿主机端口。数据库文件位于 `.cache/console-postgres`，凭证和主密钥位于 `.dev/private/console`，进程日志位于 `build/dev`。这些文件均不入 Git。初次移机先按锁定摘要准备 Go 和 PostgreSQL 镜像，不静默拉取 latest。

控制台默认 `http://127.0.0.1:19090`，模型监听 `127.0.0.1:18080`。登录令牌读取 `.dev/private/console/admin-token`，不要将内容贴到聊天、提交或日志。远程开发机使用 SSH 本地端口转发访问管理页面，例如 `ssh -L 19090:127.0.0.1:19090 <你的开发机>`，然后浏览器访问本机 19090。非回环 HTTP 不用于远程传输管理员令牌或上游 Key。

第二节点使用同一套本地数据库和主密钥：

```sh
python3 scripts/dev-console.py start --node dev-b --admin-port 19091 --data-port 18081
python3 scripts/dev-console.py status --node dev-b
python3 scripts/dev-console.py stop --node dev-b
python3 scripts/dev-console.py stop
```

停止仅对该项目已记录且身份匹配的网关进程发送正常退出信号；数据库和配置保留。管理页面草稿仅在浏览器内，未发布前刷新会丢失。默认已有实例不自动重启；更新二进制后先 stop 并等待退出，再 start。

## 日常配置

1. 渠道以紧凑列表展示，支持按名称、标识、上游地址搜索，并可按协议和配置状态筛选，默认每页 10 条，可切换为 20/50 条。点击“新增渠道”或某行“编辑”，在侧边面板填写唯一标识、名称和默认上游根地址，不填写上游 Key 或网关内部令牌。
2. 勾选实际支持的协议。多个协议可共用地址，也可分别覆盖。URL 不重复附加协议的 `/v1`；例如上游需要 `/proxy/v1/messages`，根地址填写 `https://host/proxy`。
3. New API 的渠道 Key 填写供应商真实 Key。网关从请求携带的协议认证头取得 Key，默认原样传给上游。协议设置中的“上游认证头”只调整发送给供应商的承载方式，不更换 Key。
4. 在侧边面板点击“保存到草稿”返回列表，新增/修改项会标记待发布；取消编辑不会改变草稿。列表中的“删除”同样只修改草稿。确认列表后在底部固定发布栏填写发布说明，点击“发布完整配置”。空渠道列表也可以发布，表示停用所有新请求。
5. 在节点页面确认目标节点在线且已生效最新版本。失败节点继续旧配置，不能将“发布成功”当作“所有节点生效”。
6. 在 New API 渠道 Base URL 填入对应节点地址加 `/r/<渠道标识>`，选择符合实际出站协议的渠道类型。不同 New API 记录可以共用同一网关渠道入口，并携带各自不同的 Key。
7. 修改 Key 只需修改 New API，不用发布网关版本。恢复网关历史恢复渠道、协议和错误规则设置；旧历史中的 Key 不显示、不恢复。旧 schema v2 需先确认 New API 已使用真实 Key，再重新发布为 schema 4，详见 [升级说明](credentials-passthrough.md)。

控制台采用本地 CSS/JS 资源，不依赖外部 CDN。登录页可显示/隐藏管理员令牌，登录失败会保留输入并提示原因；登录成功后清空输入，令牌只留在内存。退出时有未发布草稿会先提示确认。

顶部“最新发布”是数据库最新版本，“已同步节点”按各节点在线状态、错误和生效版本计算。顶部在途请求仅汇总在线节点最近上报的值，随每 5 秒刷新更新。读取状态失败时保留上次结果并明确提示，不能当作实时健康状态。节点详情显示节点报告的生效版本对应的配置快照。

版本历史以表格展示。详情以可折叠 JSON 树展示：默认展开配置及渠道列表，每个渠道收起并保留名称和标识；对象和数组可单独展开/折叠，也可使用“全部展开 / 全部折叠”。详情可复制/导出完整配置 JSON、对比当前草稿（按渠道标识比较字段），折叠状态不影响复制和导出的内容。复制/导出的内容为服务端过滤旧凭证后的真实配置；不导出管理员令牌。点击“恢复此版本”或“恢复并发布新版本”会弹出确认，确认后直接发布新的完整版本并替换草稿，不只是预填发布说明。

本次视觉改版按确认删除了静态稿中未实现的权重分流、连通性探针、延迟/错误率、节点 IP/地域、路径重写和独立超时；不展示示例指标或无效操作。协议覆盖仍使用现有的独立上游地址和认证头设置。历史摘要标为“配置摘要”，最新版本标为“最新发布”，避免误称为 Git Commit 或所有节点均已生效。

数据端口默认回环监听。移除内部令牌后，网关不验证调用方是否有权使用某条路由；通过专用 Docker 网络、内网访问控制或防火墙将调用来源限制为 New API。不要将数据端口开放到公网；跨机传输请求 Key 必须使用 TLS。管理页面的管理员令牌仍保留，不要将它填到 New API 渠道中。

HTTP 200 JSON 和 SSE 错误检查默认关闭，可在「错误规则」中开启并发布；开启后识别标准错误结构，仍不承诺未知格式全面防泄露。详见 [错误处理与日志](error-rules.md)。Sub2API、kiro2cc 仅按实际标准接口接入，专有异常需样本验证后增加组件。暂不开放模型列表、余额、embedding、文件、Realtime 等接口；New API 自动模型获取/部分测试功能可能不兼容，需要手工配置模型及联调。

## 启动参数

无数据库文件配置时仅运行原 bootstrap 健康模式，readyz 为 503，不开数据监听。

| 环境变量 | 内容 |
| --- | --- |
| GATEWAY_DATABASE_URL_FILE | 单行 PostgreSQL URL 文件；生产使用受信任 TLS，避免把 URL 放在进程命令行 |
| GATEWAY_MASTER_KEY_FILE | 32 字节主密钥的小写/大写十六进制文本文件，所有节点一致；加密完整快照 |
| GATEWAY_ADMIN_TOKEN_FILE | 至少 32 位管理员令牌文件；不配置则管理 API 禁用，节点同步仍可工作 |
| GATEWAY_NODE_ID | 必填唯一节点标识，小写字母、数字、横线、下划线，最长 64 字符 |
| GATEWAY_LISTEN | 数据面监听，默认 127.0.0.1:8080 |
| GATEWAY_ADMIN_LISTEN | 管理面监听，默认 127.0.0.1:9090 |

先使用具备 DDL 权限的身份执行 `gateway --migrate`；正常节点启动不做建表操作。生产应单独分配发布者和节点数据库权限，节点只读版本、写节点状态。当前节点身份由启动参数和数据库访问控制保证，不提供多租户隔离；持有同一数据库写权限的节点属于同一信任域。生产独立账号和行级限制需运维部署配置。

主密钥丢失将无法读取任何配置；不能重新生成覆盖。主密钥轮换尚未提供自动迁移，应同时备份数据库和受保护的主密钥。管理员令牌轮换需要重启管理实例；上游 Key 轮换在 New API 中完成，不修改网关配置。凭证文件必须能被容器 UID/GID 65532 读取，不能把 root 0600 文件挂给非 root 后忽略读取失败。

镜像默认非 root、scratch + CA；生产配置及秘密以只读挂载提供，数据监听仅内部网络，管理通过受控 HTTPS/SSH。示意（需先配置数据库、秘密权限和网络）：

```sh
docker run --rm --read-only --cap-drop ALL --security-opt no-new-privileges \
  --mount type=bind,source=/absolute/gateway-secrets,target=/run/secrets,readonly \
  -e GATEWAY_DATABASE_URL_FILE=/run/secrets/database-url \
  -e GATEWAY_MASTER_KEY_FILE=/run/secrets/master-key \
  -e GATEWAY_ADMIN_TOKEN_FILE=/run/secrets/admin-token \
  -e GATEWAY_NODE_ID=node-a \
  -e GATEWAY_LISTEN=0.0.0.0:8080 \
  -e GATEWAY_ADMIN_LISTEN=0.0.0.0:9090 \
  -p 127.0.0.1:19090:9090 upstream-gateway:dev
```

这里没有发布数据端口；接入 New API 时加入经验证的专用 Docker 网络。SIGTERM 最多排空 30 秒，容器 stop grace period 应大于 30 秒。

## 检查与构建

```sh
CGO_ENABLED=1 .dev/run go test -race ./...
.dev/run go vet ./...
docker build -t upstream-gateway:dev .
sh scripts/smoke-image.sh upstream-gateway:dev
```

渠道列表与侧边编辑面板的浏览器验收使用独立内存模拟 API，不连接现有网关或数据库：

```sh
PLAYWRIGHT_MODULE="$PWD/.cache/browser/node_modules/playwright" \
PLAYWRIGHT_BROWSERS_PATH="$PWD/.cache/browser-binaries" \
node scripts/browser-layout-smoke.cjs
```

需预先准备 Playwright 和 Chromium。截图与结果保存在 `build/browser-layout/`；覆盖搜索、分页、草稿保存、取消编辑、完整配置发布及移动端显示。`scripts/browser-smoke.cjs` 则会实际发布测试配置，只能用于空白的专用验收环境，不得针对已配置渠道的开发实例执行。

带真实 PostgreSQL 的集成测试（脚本只创建临时专属数据库和内部网络，完成后清理）：

```sh
docker build --target test -t upstream-gateway-tests .
sh scripts/test-database.sh upstream-gateway-tests
```

国内已缓存镜像可设置 `GATEWAY_POSTGRES_IMAGE=mirror.ccs.tencentyun.com/library/postgres@sha256:665dc7b37ac52a17e298511414c21d23ffa4e6a6ef6225b3e5d59c7afb35e994`。固定 Go 镜像的国内入口可使用 `--build-arg GO_REGISTRY=mirror.ccs.tencentyun.com/library`，标签和摘要仍固定。完整镜像构建下载 Go 依赖可使用 `--build-arg GOPROXY=https://goproxy.cn,direct`，保留校验和与 TLS 验证。

也可针对专用测试数据库运行 `.dev/run` 集成测试：将 URL 放入项目 `.dev/private` 文件，并设置 `GATEWAY_TEST_DATABASE_URL_FILE=/workspace/.dev/private/<文件名>`。测试只在自己创建的随机 schema 中读写并清理，测试账号需要 CREATE SCHEMA。禁止指向生产数据库。

合并前可在 `codex/` 分支显式手动发布候选镜像，按 [Docker 验证说明](docker-validation.md) 拉取到独立本地环境验证，通过后再合并 main。正式发布沿用版本标签或 main 上显式手动触发。普通 main 提交和 PR 只测试构建；单独推送功能分支不触发工作流。发布不部署；实际部署锁定镜像 digest。真实供应商、New API 结算/退款与目标机器容量在上线前独立验收。
