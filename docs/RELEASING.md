# 发布版本

[后端工作流](../.github/workflows/release.yml)统一发布镜像与部署包，前端使用独立仓库的同名标签。

## 准备

- 两个仓库启用 Actions，后端允许 `contents: write` 和 `packages: write`。
- 前端默认是同账号的 `stack-nest-web`。改名 / Fork 时设置 Actions Variable `STACKNEST_WEB_REPOSITORY=账号/仓库`。
- 私有前端需要 Secret `STACKNEST_WEB_READ_TOKEN`，仅授权读取其内容。

## 发布

先推送代码，再按顺序推送标签；版本号换为实际值：

先在 web 仓库执行：

```bash
git tag -a v1.0.0 -m "StackNest v1.0.0"
git push origin v1.0.0
```

再在 server 仓库执行：

```bash
git tag -a v1.0.0 -m "StackNest v1.0.0"
git push origin v1.0.0
```

后端标签触发发布。`v1.0.0-rc.1` 等标签生成预发布。也可在 **Publish release → Run workflow** 填两个仓库已存在的标签。

不要移动已发布标签或提前创建同名 Release。

## 产物

工作流固定源码提交，检查、构建并实际启动镜像，再推送 GHCR 和创建 Release。

推送前使用独立数据库与临时 TLS 邮箱，从真实浏览器完成登录、上传、预览、发布、更新和回退；任何一步失败都停止发布。部署包包含首次配置向导 `bin/stacknest-setup`。

| 产物                                | 内容                    |
| ----------------------------------- | ----------------------- |
| `stacknest-app`                     | API 与 Runner           |
| `stacknest-web`                     | 前端与 Nginx            |
| `stacknest-migrate`                 | 数据库迁移              |
| `stacknest-版本-linux-amd64.tar.gz` | Compose、模板和维护工具 |
| 同名 `.sha256`                      | 校验文件                |

镜像路径为 `ghcr.io/账号/stacknest-组件:版本`。部署包固定镜像摘要，不自动跟随 latest。

构建时把同一版本及各仓库提交号注入 API 和 Web，后台“服务器状态”可查看。手动构建镜像可传 `--build-arg VERSION=版本 --build-arg REVISION=提交号`；未传则标为 dev / unknown。镜像与部署包同时携带 LICENSE / NOTICE。

首次发布后确认三个包均为 Public，验证未登录拉取。流程不会部署使用者的服务器。

## 重试

| 失败阶段               | 处理                             |
| ---------------------- | -------------------------------- |
| 前端检出               | 仓库名、同名标签、私有仓库凭据   |
| 检查 / 构建            | 查看日志，修复代码后用新版本标签 |
| GHCR 推送              | Packages 权限、仓库关联          |
| 镜像已推送但无 Release | 修复权限或网络问题后重跑         |

完整 Release 不覆盖。浏览器失败报告保留 7 天。

[用户部署](INSTALL_RELEASE.md) · [流程原理](ARCHITECTURE.md#版本发布)
