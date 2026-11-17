> 后续扩展：原始错误已改为最多 2 KiB 加密留存，并增加 1 MiB 队列载荷预算。下文镜像标识与微基准属于扩展前的验收；本次扩展另行记录，不能将旧摘要当作新产物。

# 错误规则与日志开发验收记录

日期：2026-11-17。开发分支：`codex/gateway-console-config-versions`。本记录对应本地未发布实现，不代表远程候选或生产验收。

## 已通过

| 验证 | 结果 |
| --- | --- |
| 固定 Go 1.26.10 的 `go test -race ./...` | 通过；普通环境下 PostgreSQL 测试按要求跳过，由下项单独执行 |
| `go vet ./...`、gofmt、前端语法与 Prettier 检查 | 通过 |
| Docker 测试阶段镜像内全量竞争检测 | 通过 |
| `scripts/test-database.sh upstream-gateway-tests:error-rules` | 独立 PostgreSQL 上通过；覆盖不可变版本、节点生命周期、日志去重、同时间游标、3 天 / 50,000 条清理和日志表故障恢复 |
| `python3 scripts/check-error-rules.py` | 独立数据库与两个节点，真实浏览器/API 联调通过；完成预设、试匹配、发布、两节点同步、真实模拟上游拦截、日志、规则修改/停用/删除和历史日志定位 |
| `scripts/browser-layout-smoke.cjs` | 原有控制台回归通过，渠道列表行高仍为 52px，390px 手机宽度无整页横向溢出 |
| `scripts/smoke-image.sh upstream-gateway:error-rules-check` | 通过：存活、未配置时不就绪、无模型路由、正常退出 |

协议回归包括：Chat / Responses / Messages / Gemini 的 HTTP 与 200 JSON 错误、gzip、正常答案关键词不误拦、SSE 多行与分块、CR/LF/CRLF/BOM、首事件与中途错误、旧 SSE 持有原版本、读取超时、超限、畸形正文。错误正文与凭证不出现在安全响应或拦截元数据中。

队列测试包含正常关闭排空、队列满丢弃、超大记录丢弃、数据库失败和重试批次计入总容量。数据库测试验证日志表不可用时，转发仍返回安全错误且配置同步正常，恢复后待写记录可查询。

浏览器截图：`build/error-rules-browser/`；原有布局截图：`build/browser-layout/`。它们是忽略的本地产物。测试创建的 PostgreSQL 和测试节点均已清理，没有修改用户现有验证数据库与运行容器。

## 本地构建产物

- 镜像标签：`upstream-gateway:error-rules-check`
- 二进制版本：`dev-error-rules (unknown)`；工作区未提交，因此没有将其冒充某个已提交 revision。
- 本地镜像标识：`sha256:e368c525a97de8305417360fe2476b4200c8b2c975cf3c5cb2320860c9649bdf`
- 这是本地构建产物，不是 Docker Hub 已发布摘要，不可据此给出远程 pull 指令。候选发布仍需沿用手动 GitHub Actions 流程。

## 微基准与限制

在开发机 AMD EPYC 7K62、项目工具容器 2 CPU 配额下，1 秒微基准结果：

| 基准 | 时间 | 分配 |
| --- | --- | --- |
| 四条预设中匹配余额规则 | 368.1 ns/op | 0 B/op，0 allocs/op |
| 队列满时生成受限元数据并丢弃 | 1,867 ns/op | 560 B/op，3 allocs/op |

这些数据仅说明本次输入下的匹配和队列满路径成本，不包括 HTTP、解压、JSON/SSE 解析、PostgreSQL 写入、磁盘或 WAL。不能解释为生产延迟承诺，也不是持续全部错误场景的完整容量压测。

尚需验证：实际 New API 在自定义状态与 SSE 错误下的重试/禁用/计费行为，实际 Sub2API / kiro2cc 等供应商响应，以及目标资源限额下的 CPU、RSS、首字节延迟、数据库/WAL 增长和错误风暴日志完整性。上线前以 [实施说明](error-rules.md) 中的预算和兼容边界验收。

## 原始错误留存补充验收

按用户排查误拦需求，增加最多 2 KiB 的加密原始错误片段与管理员按需查询，队列增加 1 MiB 编码载荷上限。

- 全量 Go 竞争检测、go vet、Docker 构建与容器 smoke 通过。
- PostgreSQL 集成通过：数据库无原文、列表无原文或密文、管理员读取、未登录拒绝、旧日志标识、重复迁移与原文随事件删除。
- 加密测试通过：当前 Key 和常见凭证脱敏、随机密文、错误主密钥/篡改/跨事件替换拒绝、UTF-8 截断、边界 Key 脱敏、空正文与不可用区分、加密失败不落明文。
- 转发测试通过：HTTP / JSON / gzip 原文可解密；SSE 仅保留出错事件；解析失败和超限仍可保留前缀；下游持续只收到安全错误。
- 真实双节点浏览器联调通过：详情按需显示原文、凭证替换、HTML/script 作为文本显示；数据库和测试节点清理完成。
- 本地镜像：`upstream-gateway:error-original-check`，版本 `dev-error-original (unknown)`；本地标识 `sha256:5c4dd79a9fe07758d5a69b2e6d2fbf8ba42fbfd2af7db3abc34632982387eaec`。未发布 Docker Hub，也未部署用户运行环境。
- 截图：`build/error-rules-browser/detail.png`（真实后端测试）；`build/error-rules-preview/original-detail.png`（同步更新后的静态设计）。

本次没有重做生产容量压测；加密、原文存储与 WAL 的新增成本不包含在前述旧微基准中。
