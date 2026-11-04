# Docker 应用

仅在更新已有应用镜像、查询部署任务或配置其 CI 时读取。CLI 与后端需 `v1.0.0-rc.10+`；用 `app --help` 确认支持。

## 目标与授权

先执行 `app status`，从 Token 自动识别 `appId / slug / url / generation / image`；不要求用户补填应用名。对照本次明确目标及保存的应用 ID，不只按 slug 选凭据。

用户明确要求部署指定镜像时可直接执行，不重复确认。仅要求查询、配置 Skill 或修改代码时不自动部署。Docker 没有预览命令；用户只要预览时，不把请求改成正式更新。

镜像必须已构建并推送，使用明确标签或优先使用不可变 SHA-256 摘要。只允许更换**当前同一仓库地址的版本**；保留已有代理前缀。端口、环境变量、资源、健康路径、持久化目录和拉取凭据都沿用后台配置。更换仓库或创建应用需先在平台配置，不编造 CLI 子命令。

缺少新镜像地址时，只询问该项；不把当前镜像当成待部署的新版本。没有用户相应授权，不额外构建、推送镜像或修改项目 CI。

## CLI

下面是 Bash 示例；PowerShell 使用 `& $cli`。使用已有凭据环境变量时省略 `--token-file`。

```bash
"$cli" app status --url "$url" --token-file "$tokenFile"
"$cli" app deploy --url "$url" --token-file "$tokenFile" --image "$image"
"$cli" app status --url "$url" --token-file "$tokenFile" --generation "$generation"
"$cli" app wait --url "$url" --token-file "$tokenFile" --generation "$generation"
```

`app deploy` 直接更新正式应用，默认最多等待 75 分钟，可按需要指定 `--timeout 90m`。保留回执中的 `appId / generation / status / url`。`ready` 表示该任务已通过配置的就绪检查；非零退出码是失败，旧容器仍可访问也不代表本次更新成功。

只有用户明确要求只提交时才用 `--no-wait`；返回 `pending / deploying` 只表示已入队或正在处理，不能报告已上线。

## 中断与结果不明

- 等待超时、网络断线或 Ctrl+C 不取消后台任务。用原回执的 `generation` 执行 `app status` 或 `app wait`；不要重新 `app deploy` 来“继续”。
- 提交未确认时，CLI 错误会给出原 `request-id` 和 `expected-generation`。先核对状态；需要重试时同时沿用两者与原镜像：

  ```bash
  "$cli" app deploy --url "$url" --token-file "$tokenFile" --image "$image" \
    --request-id "$requestId" --expected-generation "$expectedGeneration"
  ```

- 缺失原编号且不能确认提交结果时，只查询并说明情况，不自动生成新请求。每应用只保留最近一次自动部署回执；409 或代次变化时停止，不跟随别人的新任务或换基线覆盖。
- 401 / 403、镜像地址、权限、容量问题不循环重试。失败可在后台“失败详情”查看启动输出；恢复旧容器不撤销数据库或文件变更。

返回确切任务状态及应用地址，按应用实际页面或用户提供的健康路径验证；后端服务不一定在根路径提供页面。访问验证失败不重新部署。

## 接入 GitHub Actions

用户要求配置 CI 时，参考[工作流模板](https://github.com/zby909/stack-nest-server/blob/main/examples/github-actions/deploy-docker.yml)，按项目实际 Dockerfile、分支和镜像仓库调整。配置 `STACKNEST_URL`、固定的 `STACKNEST_CLI_VERSION`，将应用 Token 存为 Secret `STACKNEST_DEPLOY_TOKEN`。

CI 构建并推送 Linux amd64 镜像，使用本次构建摘要部署并等待结果。同一应用串行执行，部署 Token 只注入部署步骤，不在外部 PR 中使用。代理前缀 `STACKNEST_IMAGE_PREFIX` 只加在提交给 StackNest 的地址上，推送仍到真实仓库；私有镜像拉取凭据在平台维护。
