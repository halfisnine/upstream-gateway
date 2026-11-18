> 接手快照：2026-11-16，从运维工作区 docs/ 复制。历史相对链接可能指向原运维目录；开发工具以本项目 .dev/README.md 为准。

# 上游网关开发与构建环境说明（Windows → Linux）

日期：2026-11-15（Asia/Shanghai）。配套：[详细设计](upstream-gateway-design.md)。

已新增真实 Linux 构建环境：[Instabox 构建实测记录](instabox-build-validation.md)。
固定 Go/Bun 容器、依赖校验、Linux 程序和镜像构建验证通过，网关业务源码仍待实现。

**Windows 可以构建 Linux 程序。** 本项目采用 Go 1.26.10、首版不依赖 CGO，可通过 Windows 原生 Go 交叉编译 Linux 二进制，也可使用 Docker Desktop 的 Linux 容器构建和测试。推荐后者用于正式构建，便于固定工具链并接近目标运行环境。

状态：已新增独立 `gateway/` 发布验证骨架、Dockerfile 和版本 CLI，详情见 [GitHub Actions 发布说明](gateway-ci-publishing.md)。业务转发、配置加载和校验命令仍待实现；本文保留完整目标工程的构建契约，不能将骨架编译成功视为业务实现完成，也不能把尚未实现的 CLI 失败误认为 Windows 无法构建 Linux。原构建环境实测边界见第 12 节。

## 1. 三种环境分别做什么

| 环境 | 职责 | 需要安装什么 |
| --- | --- | --- |
| Windows 本地电脑 | 编写代码、Git、查看文档；可交叉编译 | Git、编辑器；推荐 WSL2 + Docker Desktop；原生交叉编译才需 Windows Go |
| Linux 编译环境 | Linux 测试、构建二进制与镜像 | 推荐固定 Go 编译容器，可运行在 Windows Docker Desktop 或 Linux 开发机 |
| Vultr Linux 目标机 | 运行已构建的网关镜像 | Docker/Compose；**无需 Go、Bun、Buildx** |

“交叉编译”是生成另一个系统的二进制，不是直接在当前系统运行它。Linux ELF 不能在 Windows PowerShell 直接执行；可用 WSL2、Linux 容器或目标 Linux 执行。Windows 上构建 Linux 镜像需要 Linux 容器环境，单纯安装 Windows Go 不等于获得 Linux Docker 环境。

Windows on ARM、Linux arm64 与 amd64 还涉及 CPU 架构。首版参考命令面向 Windows x64 与 Linux amd64；其他机器可以交叉编译，但运行测试可能需要原生机器或仿真，不能把仿真结果当原生性能结果。

## 2. 固定工具链与目标架构

读取本项目 [toolchains.lock.json](../dev/toolchains.lock.json)，Go 固定为 1.26.10。已验证的 Linux amd64 编译镜像：

```text
mirror.ccs.tencentyun.com/library/golang:1.26.10-bookworm@sha256:5de7c99eb78ca5e515a4c340235d0955ec5964a5071c492c15503d7c267da473
```

该镜像已存在于当前开发服务器，不代表用户 Windows 上能访问腾讯内网镜像源。Windows 端如果无法访问，可尝试官方仓库相同版本和摘要：

```text
golang:1.26.10-bookworm@sha256:5de7c99eb78ca5e515a4c340235d0955ec5964a5071c492c15503d7c267da473
```

官方入口相同摘要的可拉取性仍需在实际网络验证；不得失败后自动改为 latest、其他版本或不可信镜像。可选方式是从可信开发机 `docker save` 导出既有镜像并校验传输文件哈希，Windows `docker load` 导入。load 后本地不一定保留仓库摘要关联，必须核对导出前后 image ID 与 OS/Architecture，再用本地导入标签/ID；此时 `--pull=never`，不把本地 ID 冒充仓库摘要。

后续网关自身保留独立工具链锁记录，引用同一已核对版本与摘要；不直接用现有 `dev/run` 构建网关，因为它的源码挂载固定为 New API 的 `source/`。

在目标 Linux 通过以下命令确认架构：

```bash
uname -m
docker info --format '{{.OSType}}/{{.Architecture}}'
```

| 机器输出 | Go 目标 | Docker 目标 |
| --- | --- | --- |
| x86_64 / amd64 | GOOS=linux，GOARCH=amd64 | linux/amd64 |
| aarch64 / arm64 | GOOS=linux，GOARCH=arm64 | linux/arm64 |

不能只根据“Vultr”判断架构。下文镜像模板仅覆盖 linux/amd64；arm64 发布需要对应镜像声明和原生/目标运行验证。

## 3. Windows 推荐环境准备

### 3.1 安装与检查

从官方渠道安装 Git for Windows、Docker Desktop，并按其文档启用 WSL2 后端和 **Linux containers**。WSL2 需要 Windows 版本及硬件虚拟化满足官方要求；不假定用户电脑已经配置好。

首次安装 WSL 的 PowerShell 命令（通常需要管理员权限，可能需要重启）：

```powershell
wsl --install
```

安装后在普通 PowerShell 检查：

```powershell
wsl --status
wsl --list --verbose
docker version
docker info --format '{{.OSType}}/{{.Architecture}}'
docker compose version
```

预期 Docker 服务端 OS 为 linux，WSL 发行版使用版本 2。若 Docker 引擎无法连接，先启动 Docker Desktop；若 OS 为 windows，切换 Linux containers。使用 Docker Desktop 的 WSL 集成时，不必再在同一发行版安装另一套 Docker daemon。

Docker Desktop、WSL、Git、编辑器版本按 Windows 支持情况选择并留档；本项目目前只固定 Go 编译镜像，不杜撰未核对的桌面工具版本。

### 3.2 源码目录

Windows 示例工作目录：`C:\work\snewapi`，内含 docs、dev/toolchains.lock.json，以及后续实现的 gateway 目录。实际可自选路径，下文变量集中设置。不要把整个服务器目录连同 .env、数据库 data、凭证和备份复制到本地。

可在 Windows 文件系统编辑并挂载进 Docker；如果主要使用 WSL，推荐源码放在 WSL 的 `~/work/snewapi`，减少 `/mnt/c` 上大量小文件读写。两种方式选一个主工作副本，避免修改了 A 却构建 B。

正式实施时在 gateway 内增加：

```gitattributes
* text=auto
*.go text eol=lf
*.sh text eol=lf
*.json text eol=lf
Dockerfile text eol=lf
*.ps1 text eol=lf
```

不改全局 Git 设置。生产秘密、dist、日志与缓存应加入项目忽略规则。纯 Go 网关不需要 Node、Bun 或 New API 的前端构建产物。

## 4. PowerShell 中使用固定 Linux 编译容器

### 4.1 设置路径与验证工具链

以下示例在 PowerShell 执行；路径可包含空格，使用参数数组避免手工拼接 shell 命令。`$GoImage` 默认取现有锁文件；如使用经过验证的官方入口，在当前会话显式改为上文官方镜像引用。

```powershell
$RepoRoot = (Resolve-Path 'C:\work\snewapi').Path
$GatewayDir = Join-Path $RepoRoot 'gateway'
$GoLock = Get-Content -Raw (Join-Path $RepoRoot 'dev\toolchains.lock.json') | ConvertFrom-Json
$GoImage = $GoLock.go.image

docker image inspect $GoImage --format '{{.Os}}/{{.Architecture}}'
# 如果本地没有此固定镜像，先在可达网络显式 docker pull --platform linux/amd64 $GoImage。
# 拉取失败应停止排查，不运行下面依赖该镜像的步骤。

docker run --rm --pull=never --platform linux/amd64 --network none $GoImage go version
if ($LASTEXITCODE -ne 0) { throw '固定 Go 镜像检查失败' }
```

最后应显示 `go version go1.26.10 linux/amd64`。镜像从官方源拉取时，记录实际使用入口、RepoDigest 和 image ID。大文件拉取应有合理的整体等待预算，停滞时终止后检查 DNS、连接、TLS、认证或下载问题，不无限重试。

### 4.2 工程实现后的命令包装

下面函数仅适用于后续 gateway 工程已经包含 go.mod 和源码的情况。编译缓存使用独立 Docker 卷，不和 New API 的开发数据共享；依赖代理是本次命令配置，不改系统全局设置。

```powershell
if (-not (Test-Path (Join-Path $GatewayDir 'go.mod'))) {
    throw '网关工程尚未实现或路径不正确；此时只能检查工具链'
}
$GatewayGoProxy = 'https://goproxy.cn,direct'

function Invoke-GatewayGo {
    param(
        [Parameter(Mandatory=$true)][string[]]$GoArgs,
        [switch]$Race
    )
    $CgoMode = if ($Race) { '1' } else { '0' }
    $ContainerArgs = @(
        'run', '--rm', '--pull=never', '--platform', 'linux/amd64',
        '--cpus', '2', '--memory', '2g',
        '--mount', "type=bind,source=$GatewayDir,target=/workspace",
        '--mount', 'type=volume,source=snewapi-gateway-go-mod,target=/cache/modules',
        '--mount', 'type=volume,source=snewapi-gateway-go-build,target=/cache/build',
        '--workdir', '/workspace',
        '--env', 'GOTOOLCHAIN=local', '--env', 'GOWORK=off',
        '--env', "CGO_ENABLED=$CgoMode", '--env', 'GOMAXPROCS=2',
        '--env', 'GOFLAGS=-p=2 -mod=readonly',
        '--env', 'GOMODCACHE=/cache/modules', '--env', 'GOCACHE=/cache/build',
        '--env', "GOPROXY=$GatewayGoProxy", '--env', 'GOSUMDB=sum.golang.org'
    )
    & docker @ContainerArgs $GoImage go @GoArgs
    if ($LASTEXITCODE -ne 0) { throw 'Go 容器命令失败，停止后续步骤' }
}

Invoke-GatewayGo -GoArgs @('version')
Invoke-GatewayGo -GoArgs @('mod', 'download')
Invoke-GatewayGo -GoArgs @('mod', 'verify')
Invoke-GatewayGo -GoArgs @('test', '-count=1', './...')
Invoke-GatewayGo -GoArgs @('vet', './...')
Invoke-GatewayGo -Race -GoArgs @('test', '-race', '-count=1', './...')
```

race 测试使用 Linux 原生编译容器和其 C 工具链，`CGO_ENABLED=1`；发布构建仍用 0。本次已确认固定镜像含 gcc 12.2.0，但网关的 race 测试仍待源码实现后执行。不要把 Windows→Linux 交叉编译的测试文件直接 `go test` 运行。

首版只有标准库时，go.sum 可能不存在，`go mod download` 没有外部依赖是正常情况。增加依赖时由开发流程生成和审查 go.mod/go.sum，普通构建不自动 `go get -u` 或 `go mod tidy`。

### 4.3 构建 Linux 二进制

CLI 源码应定义 `main.version`、`main.commit` 两个字符串变量供链接器注入；`--version` 输出它们以及 runtime.Version、GOOS/GOARCH。以下为开发构建，正式发布见第 9 节。

```powershell
$DistDir = Join-Path $GatewayDir 'dist'
New-Item -ItemType Directory -Force $DistDir | Out-Null
$GatewayVersion = '0.1.0-dev'
$GatewayCommit = 'unknown'
$LinkFlags = "-X main.version=$GatewayVersion -X main.commit=$GatewayCommit"

Invoke-GatewayGo -GoArgs @(
    'build', '-trimpath', '-buildvcs=false', '-ldflags', $LinkFlags,
    '-o', 'dist/gateway-linux-amd64', './cmd/gateway'
)
Invoke-GatewayGo -GoArgs @('version', '-m', 'dist/gateway-linux-amd64')
Get-FileHash (Join-Path $DistDir 'gateway-linux-amd64') -Algorithm SHA256

docker run --rm --pull=never --platform linux/amd64 --network none `
    --mount "type=bind,source=$DistDir,target=/out,readonly" `
    $GoImage /out/gateway-linux-amd64 --version
if ($LASTEXITCODE -ne 0) { throw 'Linux 二进制版本检查失败' }
```

PowerShell 的续行符是反引号，后面不能有空格；复杂命令优先沿用参数数组。含逗号的宿主机路径可能影响 Docker --mount 解析，建议使用普通工作路径。

## 5. Windows 原生 Go 交叉编译（可选）

不想运行 Docker Desktop 时，可从 [Go 官方下载](https://go.dev/dl/)安装**准确的 Go 1.26.10 Windows 版本**，按发布页提供的 SHA-256 核验安装包。没有该版本或下载不可达时先解决来源和网络，不静默替换版本。安装后新开 PowerShell，`go version` 必须是 go1.26.10。

在 gateway 源码目录执行下面的进程级环境配置。它会在结束时恢复原有变量，不使用 `go env -w` 或系统级环境修改：

```powershell
$BuildEnvNames = @('GOOS', 'GOARCH', 'CGO_ENABLED', 'GOTOOLCHAIN', 'GOWORK', 'GOFLAGS')
$SavedBuildEnv = @{}
foreach ($Name in $BuildEnvNames) {
    $SavedBuildEnv[$Name] = [Environment]::GetEnvironmentVariable($Name, 'Process')
}
try {
    $env:GOTOOLCHAIN = 'local'
    $env:GOWORK = 'off'
    $env:CGO_ENABLED = '0'
    $env:GOFLAGS = '-mod=readonly'
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $VersionText = & go version
    if ($LASTEXITCODE -ne 0 -or $VersionText -notmatch '^go version go1\.26\.10 ') {
        throw 'Go 版本必须是 1.26.10'
    }
    go test -count=1 ./...
    if ($LASTEXITCODE -ne 0) { throw 'Windows 原生测试失败' }

    $env:GOOS = 'linux'
    $env:GOARCH = 'amd64'
    New-Item -ItemType Directory -Force 'dist' | Out-Null
    go build -trimpath -buildvcs=false -o dist/gateway-linux-amd64 ./cmd/gateway
    if ($LASTEXITCODE -ne 0) { throw 'Linux 交叉编译失败' }
    go version -m dist/gateway-linux-amd64
    if ($LASTEXITCODE -ne 0) { throw '无法读取产物构建信息' }
    Get-FileHash dist/gateway-linux-amd64 -Algorithm SHA256
}
finally {
    foreach ($Name in $BuildEnvNames) {
        [Environment]::SetEnvironmentVariable($Name, $SavedBuildEnv[$Name], 'Process')
    }
}
```

该示例以 Windows x64 为前提；ARM Windows 的原生测试应使用原生架构，不照抄 amd64。仅修改 GOARCH 可以交叉生成 linux/arm64，但仍需目标运行验证。

边界：原生 Windows 单元测试 + Linux 编译成功不能替代 Linux 网络、信号、文件权限、容器测试。跨系统构建不支持直接使用目标平台 race 测试；未来依赖 CGO/C 库时还需要相应交叉 C 工具链，不能继续沿用纯 Go 假设。

## 6. WSL2 / Linux 中构建

WSL2 可以使用 Docker Desktop 集成的 docker 命令，或在专门 Linux 开发机执行同样的容器流程。不要在生产 2 GiB 主机上默认执行构建和测试。

在已经实现的 gateway 目录中，Bash 示例：

```bash
GATEWAY_GO_IMAGE='mirror.ccs.tencentyun.com/library/golang:1.26.10-bookworm@sha256:5de7c99eb78ca5e515a4c340235d0955ec5964a5071c492c15503d7c267da473'
GATEWAY_SOURCE_DIR="$PWD"
docker run --rm --pull=never --platform linux/amd64 \
  --cpus 2 --memory 2g \
  --mount "type=bind,source=$GATEWAY_SOURCE_DIR,target=/workspace" \
  --mount type=volume,source=snewapi-gateway-go-mod,target=/cache/modules \
  --mount type=volume,source=snewapi-gateway-go-build,target=/cache/build \
  --workdir /workspace \
  --env GOTOOLCHAIN=local --env GOWORK=off --env CGO_ENABLED=0 \
  --env GOMAXPROCS=2 --env 'GOFLAGS=-p=2 -mod=readonly' \
  --env GOMODCACHE=/cache/modules --env GOCACHE=/cache/build \
  --env 'GOPROXY=https://goproxy.cn,direct' --env GOSUMDB=sum.golang.org \
  "$GATEWAY_GO_IMAGE" sh -eu -c '
    go version
    go mod download
    go mod verify
    go test -count=1 ./...
    go vet ./...
    mkdir -p dist
    go build -trimpath -buildvcs=false -o dist/gateway-linux-amd64 ./cmd/gateway
    ./dist/gateway-linux-amd64 --version
    sha256sum dist/gateway-linux-amd64
  '
```

Linux bind mount 下产物可能由容器 root 创建；只修复自己生成的 dist 权限或使用合适 UID，不递归修改生产目录。Windows Docker Desktop 的文件权限映射不同，生产应再次设置 Linux 文件权限。

## 7. 构建 Linux 运行镜像

### 7.1 工程 Dockerfile 模板（待创建）

下面作为 gateway/Dockerfile 的实施模板。只支持本阶段 linux/amd64；使用 Linux builder 测试，再生成静态二进制。运行层使用 scratch，加上固定 builder 中的 CA 根证书，避免缺少 CA 导致 HTTPS 上游失败。

```dockerfile
ARG GO_IMAGE=mirror.ccs.tencentyun.com/library/golang:1.26.10-bookworm@sha256:5de7c99eb78ca5e515a4c340235d0955ec5964a5071c492c15503d7c267da473
FROM ${GO_IMAGE} AS build
ARG VERSION=0.1.0-dev
ARG COMMIT=unknown
ENV GOTOOLCHAIN=local GOWORK=off CGO_ENABLED=0 GOMAXPROCS=2
ENV GOFLAGS="-p=2 -mod=readonly"
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY} GOSUMDB=sum.golang.org
WORKDIR /src
COPY . .
RUN test "$(go env GOOS)/$(go env GOARCH)" = "linux/amd64"
RUN go mod download && go mod verify
RUN go test -count=1 ./... && go vet ./...
RUN mkdir -p /out && go build -trimpath -buildvcs=false \
    -ldflags="-X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /out/gateway ./cmd/gateway

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chmod=0555 /out/gateway /gateway
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/gateway"]
CMD ["serve", "--config", "/etc/gateway/config.json"]
```

不把配置与凭证 COPY 进镜像。scratch 没有 shell、curl、包管理器，健康检查通过未来 gateway healthcheck 子命令执行。无需动态库，但 HTTPS 仍需 CA；未来需要额外文件必须明确加入，不能关闭证书校验。

### 7.2 构建上下文白名单

必须同时创建 gateway/.dockerignore，且 `docker build` 的上下文严格为 gateway 目录：

```dockerignore
**
!go.mod
!go.sum
!cmd/
!cmd/**
!internal/
!internal/**
!testdata/
!testdata/**
!config.example.json
!Dockerfile
!.dockerignore
```

testdata 仅放虚构测试数据，不含真实凭证或聊天。以后增加必要源码目录需显式加入白名单。不得用根目录 `COPY . .` 打包 New API、生产 .env 或备份。

### 7.3 PowerShell 构建并验证

沿用第 4 节 `$GatewayDir` 与经验证的 `$GoImage`，工程模板落地后执行：

```powershell
$GatewayVersion = '0.1.0-dev'
$GatewayCommit = 'unknown'
$GatewayImage = "snewapi-upstream-gateway:$GatewayVersion"
$BuildArgs = @(
    'build', '--platform', 'linux/amd64',
    '--build-arg', "GO_IMAGE=$GoImage",
    '--build-arg', "VERSION=$GatewayVersion",
    '--build-arg', "COMMIT=$GatewayCommit",
    '-t', $GatewayImage, $GatewayDir
)
& docker @BuildArgs
if ($LASTEXITCODE -ne 0) { throw '镜像构建失败' }

docker image inspect $GatewayImage --format '{{.Os}}/{{.Architecture}} {{.Id}}'
if ($LASTEXITCODE -ne 0) { throw '镜像检查失败' }
docker run --rm --network none --read-only --cap-drop ALL $GatewayImage --version
if ($LASTEXITCODE -ne 0) { throw '镜像版本检查失败' }
```

此版本检查不会接触生产秘密和真实模型。完整验收还需要模拟上游、非 root 配置读取、SSE、计费与真实网络验证。原生 Windows 构建的 Linux 二进制也可打包进 Linux 镜像，但本模板选择容器内编译，减少来源混淆。

## 8. 模拟开发、配置校验与真实接入

工程实现后先使用独立测试令牌、模拟上游和隔离 New API 开发环境。生产上游凭证不放入测试样本或命令行。建议开发监听仅发布 `127.0.0.1`，正式服务仅内部 Docker 网络。

未来 CLI 使用：

```text
gateway --version
gateway check --config /etc/gateway/config.json
gateway serve --config /etc/gateway/config.json
gateway healthcheck --url http://127.0.0.1:9090/readyz
```

config 的绝对路径相对于运行环境；Linux 容器里的 `/run/secrets/...` 不是 Windows `C:\...`。使用只读 bind mount 映射；生产 Linux 确保 UID 65532 能读取配置/凭证，不能把只允许 root 读的文件直接挂给非 root 后忽略启动失败。

仅运行 `--version` 不代表配置或功能可用。check 成功也不代表上游连通；模型调用成功又不代表计费与取消路径验证完成，分层留档。

## 9. 发布、传输与目标机运行

正式构建需要源码提交。当前运维仓库尚未建立可用于该网关发布的源码提交，网关也未实现；开发示例的 unknown 不能用于正式发布。未来先审查并提交可公开源码，秘密排除跟踪，然后取得实际 commit；构建期间不修改源码。

发布记录至少包括：版本、源码完整提交、工作区是否干净、Go 版本、builder 镜像引用与 ID、GOOS/GOARCH、CGO 设置、依赖锁、测试结果、二进制哈希、运行镜像 ID、推送后的 RepoDigest（若有）、配置 schema 和兼容范围。

两种传输方式：

| 方式 | 做法 |
| --- | --- |
| 镜像仓库 | 上传不可变版本，记录真实仓库 digest；目标按 digest 拉取。仓库地址与权限确定后再配置 |
| 离线镜像包 | Windows docker save，计算 SHA-256，安全传输；目标核验后 docker load，核对 image ID/架构 |

离线方式的 PowerShell 示例（镜像已经构建后）：

```powershell
$ImageArchive = Join-Path $GatewayDir 'dist\gateway-linux-amd64-image.tar'
New-Item -ItemType Directory -Force (Split-Path $ImageArchive) | Out-Null
docker save --output $ImageArchive $GatewayImage
if ($LASTEXITCODE -ne 0) { throw '镜像导出失败' }
Get-FileHash $ImageArchive -Algorithm SHA256
```

通过可信渠道记录该哈希并传输文件。目标 Linux 核验和导入：

```bash
sha256sum gateway-linux-amd64-image.tar
# 与发布记录中的 SHA-256 比较一致后，再执行下面的导入。
docker load --input gateway-linux-amd64-image.tar
docker image inspect snewapi-upstream-gateway:0.1.0-dev --format '{{.Os}}/{{.Architecture}} {{.Id}}'
docker run --rm --network none --read-only --cap-drop ALL snewapi-upstream-gateway:0.1.0-dev --version
```

示例 tag 为开发版，正式替换为发布记录的实际不可变版本。文件 SHA-256、镜像 ID、仓库 manifest digest 是三个不同概念，不相互替代。不要使用 `docker import` 导入 save 包，它不是 load 的等价替换。

正式运行 Compose、配置文件与秘密挂载将在开发交付时提供；先做候选镜像与测试渠道验证，不能用以上命令自动修改 New API 生产渠道。目标机只运行和加载镜像，不需要构建工具链。

## 10. 国内网络与常见问题

| 问题 | 处理 |
| --- | --- |
| 腾讯镜像地址在 Windows 不通 | 区分内网来源限制；验证官方相同 digest 或可信离线导出，不改版本 |
| Docker Hub/模块代理失败 | 分别检查 DNS、连接、TLS、HTTP/认证与大文件下载；单次/项目代理，有限重试 |
| 镜像拉取成功，Go 依赖失败 | 两条网络路径不同；单独配置 GOPROXY，保留 GOSUMDB 和 TLS 校验 |
| PowerShell 运行 Linux 文件失败 | 它是 ELF，使用 WSL2/Linux 容器，不是改成 .exe |
| `exec format error` | 核对 Linux/Windows 格式与 amd64/arm64，检查镜像实际架构 |
| Go 自动尝试下载其他版本 | 检查 go.mod 和 GOTOOLCHAIN=local，使用精确 1.26.10 |
| `-race` 报 CGO 相关错误 | 原生 Linux 测试容器使用 CGO_ENABLED=1 和 C 编译器；发布仍用 0 |
| HTTPS 提示未知证书 | 检查运行镜像 CA；企业代理使用受信任的 CA 配置，不关闭验证 |
| Shell 脚本出现 `\r` 错误 | 按项目 .gitattributes 使用 LF |
| 配置文件权限拒绝 | 核对容器 UID 65532 和 Linux 秘密文件属主/权限；不要全目录 chmod 777 |

下载超时后先报告失败层次，不将一次超时当成永久不可达，不将首页访问成功当成镜像/依赖下载成功。宿主机和容器的代理/DNS 配置不同，需要分别验证。

## 11. 开发环境验收清单

- 固定编译容器实际输出 Go 1.26.10，来源和摘要/导入 ID 已记录。
- Docker 为 Linux 模式，目标 CPU 与 GOARCH 一致。
- 网关工程存在，依赖可按锁定下载，测试和 vet 通过；并发组件的 Linux race 检查通过。
- Linux 二进制构建信息为预期 GOOS/GOARCH、CGO_ENABLED=0，能够在 Linux 执行版本命令。
- 镜像构建上下文没有秘密，非 root、CA、只读运行及健康检查验证通过。
- 真实上游、New API 计费和目标容量另行验收，不混入“工具链可用”的结论。

## 12. 本次实际验证与未验证范围

2026-11-15，在当前 Linux 开发服务器的临时目录创建了仅使用标准库的工具链验证程序（包含 net/http，无网关业务、无外部请求），使用已锁定镜像，`--network none`、CGO_ENABLED=0：

| 验证 | 结果 |
| --- | --- |
| 工具链执行 | Go 1.26.10 linux/amd64 |
| linux/amd64 构建 | 成功；file 确认为 x86-64 静态 ELF |
| linux/amd64 执行 | 成功，输出 `toolchain-probe go1.26.10 linux/amd64 GET` |
| windows/amd64 交叉编译 | 成功；file 确认为 Windows PE32+，未在 Windows 执行 |
| linux/arm64 交叉编译 | 成功；file 确认为 AArch64 静态 ELF，未在 ARM 机器执行 |
| 构建信息 | go version -m 显示 Go 1.26.10、CGO_ENABLED=0、GOOS=linux、GOARCH=amd64 |
| 文档 Dockerfile 模板 | 用同一临时小程序按模板构建成功；Go 版本与版本/提交注入正确 |
| scratch 运行模板 | linux/amd64，UID/GID 65532；无网络、只读根文件系统、移除 capabilities 后小程序运行成功 |
| 构建上下文白名单 | Go 源码进入 builder，虚构 private/EXCLUDE_ME 标记未进入；builder 含非空 CA bundle |
| 文档检查 | JSON 配置引用、文档本地链接和代码围栏检查通过；Bash 示例通过 bash -n 语法检查 |

此验证证明现有编译容器能产出对应格式、文档镜像模板可以构建纯 Go 小程序，不证明用户 Windows 的 WSL2/Docker 已配置，也不等同于 Windows 主机交叉编译实测。本环境没有 PowerShell，本文 PowerShell 命令未实际执行。临时小程序没有网关业务或测试用例，模板中的 go test/vet 通过不构成网关功能测试。网关真实源码尚不存在，完整工程构建、HTTPS 上游连接、功能和生产镜像仍需在实现后验证。

## 13. 官方参考

- [Go 构建环境变量](https://pkg.go.dev/cmd/go#hdr-Environment_variables)：GOOS、GOARCH 等。
- [Go CGO 文档](https://pkg.go.dev/cmd/cgo)：交叉编译、CGO_ENABLED 和 C 工具链要求。
- [Go 下载](https://go.dev/dl/)：安装包、精确版本与校验值。
- [Microsoft WSL 安装说明](https://learn.microsoft.com/windows/wsl/install)。
- [Docker Desktop WSL2 后端](https://docs.docker.com/desktop/features/wsl/)。
- [Docker 多平台构建](https://docs.docker.com/build/building/multi-platform/)：平台、仿真与 Go 交叉编译区别。
