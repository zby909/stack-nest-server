# 本机配置

这是随 Skill 分发的通用配置说明，没有默认服务器或凭据。

安装后可编辑本机 Skill 目录中的这份文件，记录以下非敏感信息：

- CLI：`stacknest-deploy` / `stacknest-deploy.exe` 的可执行文件路径，或已配置的 PATH 命令。
- 管理端：用户自己的 StackNest HTTPS 地址，也可通过 `STACKNEST_URL` 提供。
- 项目凭据：由用户项目配置或本次请求提供 Token 文件路径，也可使用已有的 `STACKNEST_DEPLOY_TOKEN` 环境变量。不要在此记录 Token 原文。

如果有多个站点或凭据，先明确用户要部署的目标，再调用 `status` 核对绑定项目。不要为一个新项目自动选用上次使用的 Token。
