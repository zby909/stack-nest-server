---
name: stacknest-deploy
description: "通过 StackNest CLI 将 HTML、HTM、ZIP 或 RAR 上传到已有静态项目，生成预览、续传失败的上传、查询版本或发布指定草稿。用户要求把生成的 HTML 或静态网站部署到 StackNest 时使用；不用于 Docker 应用部署或安装 StackNest 平台。"
---

# StackNest 部署

使用 `stacknest-deploy` 完成静态项目的自动部署。CLI 负责分片、文件校验、续传及有限重试；不用重新实现上传协议。

## 确定本次操作

- “给我预览”“先看看”：只执行 `upload`，返回预览链接。
- “直接上线”“更新正式网站”：用户已授权正式发布；上传成功后发布这次产生的确切版本，无需重复确认。Token 也须有发布权限。
- 未说明是否正式上线时，默认生成预览，并说明正式站点尚未变化。
- “发布刚才那个预览”：使用该次上传回执中的 `draftId`、`expectedId`，不要重新上传文件或盲目发布最新草稿。没有回执且无法确定用户指定的版本时，先澄清目标。
- 查询项目或版本时执行 `status`，不产生上传或发布。

## 准备目标和文件

1. 读取 [本机配置](references/local-config.md)。本次用户指定优先；其次使用用户项目中明确的配置，再使用本机默认值。明确 CLI 路径、管理端 URL、项目对应的 Token 文件路径或已注入的 `STACKNEST_DEPLOY_TOKEN`。缺少信息时只询问缺少的项，不搜索其他项目的凭据。
2. 使用 `version` / `--help` 确认 CLI 可执行。需要 `v1.0.0-rc.2` 或更新的兼容版本。CLI 缺失时按用户的环境获取匹配系统和架构的 [发布包](https://github.com/zby909/stack-nest-server/releases)，校验配套 SHA-256 后使用。
3. 凭据通过 `--token-file` 或已有环境变量传入，让 CLI 读取。不要输出 Token、把它拼进命令行或存入 Skill、源码、Git 和聊天。远程地址使用 HTTPS；HTTP 仅用于本机回环隧道。
4. 用同一份配置执行 `status`，核对返回的项目 `id` / `slug` 与用户意图一致。一个 Token 仅绑定一个已有项目。新项目或新 Token 要由用户在后台创建，不能借用其他项目的凭据。
5. 确认上传的是用户指定的成品。支持 HTML / HTM / ZIP / RAR，单文件不超过 90 MiB。单个 HTML 名称不限，会作为 `index.html`；依赖本地 CSS、JS、图片时应将相关资源连同 `index.html` 打包。前端源码项目须先执行它自身的构建步骤，再打包输出目录内容；不把源码、`.env`、凭据或整个工作目录打包。

本 Skill 只处理现有静态项目的部署；创建、删除、回退、丢弃草稿、管理 Token 和 Docker 应用仍走平台对应功能。

## 调用 CLI

在用户项目目录执行，让默认续传收据留在该项目。下面的变量来自已确认的路径和配置；按当前终端正确传递参数，不拼接可执行的命令字符串。

PowerShell 示例：

```powershell
& $cli status --url $url --token-file $tokenFile
& $cli upload --url $url --token-file $tokenFile --file $file --notes $notes
& $cli publish --url $url --token-file $tokenFile --draft-id $draftId --expected-id $expectedId
```

Bash 示例：

```bash
"$cli" upload --url "$url" --token-file "$tokenFile" --file "$file" --notes "$notes"
```

使用已注入的 `STACKNEST_DEPLOY_TOKEN` 时省略 `--token-file`。其他子命令的参数相同。`publish` 仅在本次任务包含正式发布时执行。

- 检查退出码；非零就停止依赖该结果的操作。标准输出是 JSON，进度在标准错误输出；不要合并两者再解析 JSON。
- `upload` 默认生成预览，并返回 `projectId`、`slug`、`draftId`、`expectedId`、`previewUrl`、`siteUrl`、`status`。更新说明最多 4096 字节。
- 保留该次返回值供发布使用。只有这份回执的 `expectedId` 为空时，发布参数才传字面值 `none`；非空时原样传入。不能为消除冲突而改成查询到的新版本编号。
- 上传仅返回 `status=preview` 时，使用其 `previewUrl`；重复执行同一次已发布上传可能返回 `status=published`，此时核对 `siteUrl` 和版本，不生成新草稿。

## 中断、草稿和重试

- 未完成上传保留 24 小时。默认收据为当前目录的 `.stacknest-upload-*.json`；保持同一工作目录、文件和参数才能续传。上传期间不修改文件，成功后也保留收据以便幂等确认。
- CLI 已内置有界重试。遇到网络错误且连接已恢复，可以用相同命令再试一次；仍失败就报告已确认的状态，不在外层无限循环。
- 同一项目只能有一个草稿。已有其他草稿、版本冲突或恢复信息不匹配时停止，并说明需在后台核实；不自动丢弃草稿、改变版本基线或删除收据来强行覆盖。
- 401 / 403、文件校验错误、容量不足等情况不自动重试。说明实际错误及对应处理，不展示凭据。
- 默认按文件和参数生成新收据，无需手动指定 `--state`。如果用户指定固定状态文件，新文件必须使用新的状态文件。
- 凭据和 `.stacknest-upload-*.json` 不提交到 Git；尊重用户项目现有的忽略规则。

## 验证与交付

访问返回的预览或正式 URL，核对 HTTP 状态和本次内容的可辨识特征，例如标题或修改的文字。不能访问时如实区分“上传／发布成功”和“访问验证未完成”，不要因为验证失败再次发布。

返回实际项目、预览或正式状态、访问链接及必要的版本编号。预览地址可由持有链接的人访问；只上传预览时明确正式站点未变化。正式发布后项目原网址保持不变。
