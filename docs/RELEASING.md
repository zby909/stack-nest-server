# GitHub Actions 版本发布

后端仓库的 [release.yml](../.github/workflows/release.yml) 统一构建和发布三个镜像及部署包。前端仍有独立仓库，正式发布使用两边同名的版本标签；开始执行时解析并记录各自的完整提交 SHA，后续检查和构建都使用该 SHA。

## 一次性准备

1. 启用两个仓库的 GitHub Actions。后端发布工作流需要 `contents: write` 和 `packages: write`；权限已按 job 声明，组织策略也须允许。
2. 前端默认位于同一 GitHub 账号下的 `stack-nest-web`。若仓库名不同，在后端 **Settings → Secrets and variables → Actions → Variables** 设置 `STACKNEST_WEB_REPOSITORY=账号/仓库`。
3. 前端为公开仓库时不需要额外凭据。尚未公开时，在后端添加 `STACKNEST_WEB_READ_TOKEN` Secret，使用仅允许读取前端仓库内容的细粒度 Token；默认 `GITHUB_TOKEN` 不能读取另一个私有仓库。不要把 Token 写入配置文件。
4. 首次成功发布后，在账号的 Packages 页面把 `stacknest-app`、`stacknest-web`、`stacknest-migrate` 三个包设为 Public，外部用户才能匿名拉取。公开源码不会自动保证镜像也已公开。组织或套餐策略可能限制这个设置。

发布使用 GitHub 提供的 `GITHUB_TOKEN`，不需要另存 Docker Hub 密码。镜像命名为 `ghcr.io/<后端仓库所有者>/stacknest-{app,web,migrate}`；三个包都关联后端仓库。Fork 后按 Fork 所有者发布，前端仓库可通过上述变量指定。

参考：[GHCR 权限和可见性](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)、[发布 Docker 镜像](https://docs.github.com/en/actions/tutorials/publish-packages/publish-docker-images)。

## 发布一个版本

先提交、推送希望发布的前后端代码。未提交的本地改动不会进入 Actions，也不会自动同步到 GitHub。

例如发布 `v1.0.0`：**先在前端创建并推送标签，再推送后端同名标签**。命令中的版本号只是示例，不代表该版本已经发布：

```bash
# 在 web 仓库，对已确认的前端提交打标签
git tag -a v1.0.0 -m "StackNest v1.0.0"
git push origin v1.0.0

# 再在 server 仓库，对配套后端提交打标签
git tag -a v1.0.0 -m "StackNest v1.0.0"
git push origin v1.0.0
```

后端的标签推送自动启动发布。也可在后端 **Actions → Publish release → Run workflow** 填已存在于两个仓库的版本标签；选择包含发布工作流的分支，实际源码仍按所填标签检出。支持 `v1.0.0-rc.1` 等预发布版本。

不要移动已发布标签，也不要先手工创建 GitHub Release。工作流最后统一创建 Release，发现同版本已存在时会停止。

## 发布前会检查什么

1. 复用后端检查工作流：真实 PostgreSQL 数据库迁移和集成测试、Go race 测试、vet、依赖漏洞检查、部署工具测试及构建。
2. 检出选定前端提交，安装锁定依赖，运行类型检查、lint 和桌面/手机浏览器回归。
3. 构建 API/Runner、Nginx/前端、Atlas 迁移三个 Linux amd64 镜像，以及可选宿主网络服务二进制。
4. 用独立 Compose 项目、空数据卷和随机回环端口实际启动镜像，验证迁移退出码、前端页面、数据库健康、Runner、未登录 401 和未知域名 404；之后清理本次测试资源。
5. 对 Web 镜像运行 Nginx 入口检查，覆盖普通 HTTP、TLS、SSH、Tunnel 及缓存行为。
6. 所有检查通过才登录 GHCR、推送镜像，记录 registry digest 并打包，最后创建 GitHub Release。

临时启动检查使用虚构邮箱和专用测试密码，不接触实例配置、不发邮件。它验证发布镜像能协同启动；不能代替每个用户服务器上的 DNS、SMTP 收信、证书和业务验收。

## 发布物与使用方法

每个版本创建：

| 发布物                                | 内容                                                  |
| ------------------------------------- | ----------------------------------------------------- |
| `stacknest-app:vX.Y.Z`                | API 与 Runner 共用                                    |
| `stacknest-web:vX.Y.Z`                | Nginx 与前端构建结果                                  |
| `stacknest-migrate:vX.Y.Z`            | Atlas 与本版数据库迁移                                |
| `stacknest-vX.Y.Z-linux-amd64.tar.gz` | Compose、Nginx 模板、环境模板、维护工具及可选联网服务 |
| 同名 `.sha256` 文件                   | 部署包校验和                                          |

部署包只从固定公开文件清单生成，不扫描整个仓库，不包含本地 `.env`、证书、授权码、数据库或业务文件。内部 `SHA256SUMS` 可校验每个文件；`release.json` 记录两边源码提交和三个镜像摘要。

环境模板固定为 `ghcr.io/...@sha256:...`；API 与 Runner 使用完全相同的镜像。不会自动跟随 `latest`，不会自动升级用户服务器。当前只提供 amd64，和 Docker 应用运行器支持的架构一致。

使用者按 [版本镜像部署](INSTALL_RELEASE.md) 下载、配置并启动。源码构建仍可按 [源码部署教程](GETTING_STARTED.md)，已有离线迁移包仍可使用本地 `stacknest-*:<版本>` 镜像。

## 失败和重试

- 前端标签不存在：先推送匹配的前端标签，再重新运行工作流。
- 前端检出 404：检查仓库名称和私有仓库只读 Token 权限。
- GHCR 推送被拒绝：检查 Packages 写权限；同名包已存在但未关联仓库时，先授予后端仓库 Actions 访问权限。
- 检查失败：本次不会创建 Release；浏览器失败报告保留 7 天。修复代码应使用新版本标签。
- 推送途中失败：GHCR 可能已存在部分镜像，但没有完整部署包；重跑未完成的发布即可。已经成功创建 Release 的版本不能覆盖，应发布新版本。
- GitHub Release 创建失败但三个镜像已推送：核对权限和同名 Release 状态；不要给用户交付半成品。未创建 Release 时可重新运行。
- 私有仓库或大量发布可能消耗 Actions 分钟和 Packages 配额，按 GitHub 账号的实际额度管理。

整个发布流程只访问 GitHub/GHCR，不需要 SSH 服务器密码、SMTP 授权码或生产数据库凭据，也不会自动部署当前运行的服务器。
