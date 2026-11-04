# 静态站点

仅在静态上传、预览、草稿更新或正式发布时读取。

## 操作与文件

- “给我预览”或未说明上线：只 `upload`；正式站点未变化。
- “直接上线”“更新正式网站”：已授权发布，上传后使用该次回执直接 `publish`，不重复确认。
- “发布刚才那个预览”：使用对应回执，不重新上传；无法确定指定版本时先澄清。
- “继续修改这个预览”：使用指定草稿的 `draftId` 作为 `upload --replace-draft`，默认仍只预览，不自动替换最新草稿。

上传用户指定的成品。HTML / HTM / ZIP / RAR 最大 90 MiB；单 HTML 名称不限，发布为 `index.html`。依赖本地资源时，将相关资源和 `index.html` 一起打包。源码项目先按自身方式构建，只打包输出，不带 `.env`、凭据或整个源码目录。

## CLI

变量来自已验证的配置。下面是 Bash 示例；PowerShell 使用 `& $cli`，参数分别传递。

```bash
"$cli" status --url "$url" --token-file "$tokenFile"
"$cli" upload --url "$url" --token-file "$tokenFile" --file "$file" --notes "$notes"
"$cli" upload --url "$url" --token-file "$tokenFile" --file "$file" --replace-draft "$oldDraftId"
"$cli" publish --url "$url" --token-file "$tokenFile" --draft-id "$draftId" --expected-id "$expectedId"
```

使用已注入的 `STACKNEST_DEPLOY_TOKEN` 时省略 `--token-file`。更新说明最多 4096 字节。

`upload` 回执包含 `projectId / slug / draftId / expectedId / previewUrl / siteUrl / status`。保留本次回执；仅当其 `expectedId` 为空时，发布参数才传字面值 `none`。不重新查询新基线来消除冲突。

`status=preview` 使用 `previewUrl`。同一次上传已经发布时可能返回 `published`，核对正式版本和 `siteUrl`，不再制造草稿。正式更新或回退保持原网址。

## 草稿与中断

- 替换草稿需 CLI / 后端 `v1.0.0-rc.4+`；先确认帮助中有 `--replace-draft`。校验新文件成功后才替换；成功使用**新回执和新预览地址**，后续发布也使用新编号。不以“先丢弃再上传”代替。
- 在用户项目目录运行，默认 `.stacknest-upload-*.json` 保存续传收据。任务保留 24 小时；保持同一工作目录、文件和参数，上传期间不改文件，成功后也保留回执。
- CLI 已内置有限重试。连接恢复后可用相同命令再试一次；仍失败则报告已确认状态，不在外层循环。
- 已有其他草稿、指定草稿或正式版变化、恢复信息不匹配时停止。不改成新草稿编号、不删除收据或丢弃草稿强行覆盖。
- 默认无需 `--state`；用户指定固定状态文件时，新文件必须使用新状态文件。凭据和收据不提交到 Git。

核对返回地址的 HTTP 状态及本次内容，例如标题或改动文字。只有预览时明确正式站点未变化；版本清理后预览地址可能失效。
