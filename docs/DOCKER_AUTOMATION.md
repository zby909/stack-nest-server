# 自动部署 Docker 应用

在 CI 构建并推送镜像，再让 StackNest 更新已有应用。CLI 默认等待部署及健康检查完成，失败返回非零退出码。需要后端、Web 和 CLI `v1.0.0-rc.10` 或更新版本。

## 准备

1. 在后台创建 Docker 应用，配置好端口、环境变量、数据目录、健康路径和私有仓库凭据。
2. 打开应用的“自动部署”，创建 Token；它只绑定此应用，但可直接更新正式服务。
3. Token 存入私有文件或 GitHub Secret，不写进代码、聊天或命令行参数。

Token 原文只显示一次，默认 30 天有效，最多保留 10 个，可随时撤销。过期、创建者失去权限或应用归属变化后，后续请求被拒绝。撤销不会取消已接受的后台任务。

## 交给 AI

在应用“自动部署”中点击“交给 AI 部署”，下载通用 Skill 并复制说明，再提供新镜像地址及应用 Token 文件路径。已安装者更新整个 Skill 目录，保留自己的私有配置。

可以说：“使用 stacknest-deploy 将这个镜像部署到我的应用，等检查通过后给我链接。”首次连接会自动识别 Token 绑定的应用，不必填写应用名；断线后说“继续等待刚才的部署”，使用原任务代次，不重复提交镜像。

此引导随 `v1.0.0-rc.11` 提供，使用已有 `rc.10+` CLI 和部署接口。

## 本机使用

从 [Releases](https://github.com/zby909/stack-nest-server/releases) 下载对应系统的 CLI，校验后解压。设置 `STACKNEST_URL` 为管理端 HTTPS 地址；本机 SSH 隧道也可使用 `http://localhost:18080`。

```bash
stacknest-deploy app status --token-file /私有目录/app.token
stacknest-deploy app deploy --image ghcr.io/owner/app@sha256:实际摘要 \
  --token-file /私有目录/app.token
```

PowerShell 使用 `./stacknest-deploy.exe`。也可通过环境变量 `STACKNEST_DEPLOY_TOKEN` 注入 Token。

只允许更换**同一仓库地址的标签或摘要**，例如 `ghcr.io/owner/app:v1` → `ghcr.io/owner/app@sha256:…`。推荐不可变摘要；更换仓库或代理地址须在后台操作。端口、环境变量、资源、健康路径、数据目录及拉取凭据均沿用后台配置。

标准输出为 JSON，进度写到标准错误。`deploy` 默认最多等待 75 分钟，可用 `--timeout 90m` 调整。`--no-wait` 仅返回入队回执，不代表部署成功。

断线或 Ctrl+C 不取消服务器任务。用回执中的代次继续查询或等待：

```bash
stacknest-deploy app status --generation 8 --token-file /私有目录/app.token
stacknest-deploy app wait --generation 8 --token-file /私有目录/app.token
```

代次变化时停止，不跟随其他更新。提交请求自动有界重试且保持原请求编号；若提交未确认，先查看后台，再按 CLI 输出的原 `--request-id` 和 `--expected-generation` 重试。每应用只保存最近一次提交回执，不能把旧任务重放为新部署。

## GitHub Actions

将 [工作流模板](../examples/github-actions/deploy-docker.yml) 复制到**用户应用仓库**的 `.github/workflows/deploy.yml`，仓库中应已有 Dockerfile。

| 配置 | 内容 |
| --- | --- |
| Variable `STACKNEST_URL` | 管理端 HTTPS 地址，Runner 必须能访问 |
| Variable `STACKNEST_CLI_VERSION` | 固定 CLI 版本，如 `v1.0.0-rc.10` |
| Secret `STACKNEST_DEPLOY_TOKEN` | 本应用的部署 Token |
| Variable `STACKNEST_IMAGE_PREFIX`（可选） | 镜像代理前缀，如 `gh-proxy.com/docker/`；直连留空 |

模板在 `main` 推送或手动运行时，构建 Linux amd64 镜像、推送 GHCR，并把本次构建的摘要交给平台。默认镜像地址为小写的 `ghcr.io/账号/仓库`，须与后台所填仓库相同；按实际 Dockerfile、构建参数和主分支调整。

后台使用镜像代理时，前缀只用于提交给 StackNest 的地址；构建推送仍发往 GHCR。

私有 GHCR 镜像仍需在 StackNest 后台配置可拉取该包的凭据，构建任务的 `GITHUB_TOKEN` 不会自动交给服务器。部署 Token 只注入部署步骤；模板不在 Pull Request 上使用它，并串行部署同一仓库的应用。

更新失败仍沿用平台已有的旧容器恢复逻辑，**不撤销应用数据库或文件变更**。原理见 [Docker 应用](DOCKER_APPS.md)，接口见 [自动部署协议](../openapi/app-automation.yaml)。静态站点继续使用[原有流程](AUTOMATION.md)。
