# 自动部署静态项目

用本机脚本、AI 或 GitHub Actions 上传 HTML / ZIP / RAR。先生成预览，确认后发布到原项目网址。[Docker 自动部署](DOCKER_AUTOMATION.md)使用应用专属 Token。

## 1. 创建项目凭据

从零开始时，在项目页点 **创建空项目**，填好名称后会直接进入设置。已有项目从 **项目详情 → 设置** 进入。点击 **自动部署 → 创建部署 Token**。

- 默认有效期 30 天、**仅预览**。这适合让 AI 上传后由你确认。
- 需要脚本直接上线时，勾选 **允许正式发布**；调用方仍须明确执行发布命令。
- 原文只显示一次，保存在本机私有文件或 GitHub Secret。每个项目最多保留 10 个 Token，不用的或过期的可以撤销。

Token 只允许在绑定的已有项目上传预览，或按授权发布。项目归属改变、项目删除、创建者失去权限，以及 Token 撤销或过期，都会阻止后续请求。

## 2. 准备命令行工具

按 [CLI 下载与校验](README.md#下载-cli)取得与平台配套的工具。

## 3. 上传预览

把 Token 单独保存到仓库外的文件，例如 `~/.config/stacknest/travel.token`，文件内容只有 Token。Linux / macOS 将文件权限设为 `600`；Windows 放在仅自己账号可读的目录。不要把真实 Token 复制进命令历史或聊天。

**PowerShell：**

```powershell
$env:STACKNEST_URL = 'https://stack-nest.example.com'
.\stacknest-deploy.exe upload --file '.\旅行攻略.html' --token-file "$HOME\.config\stacknest\travel.token" --notes '补充第二天行程'
```

**Bash：**

```bash
export STACKNEST_URL=https://stack-nest.example.com
./stacknest-deploy upload --file ./旅行攻略.html --token-file "$HOME/.config/stacknest/travel.token" --notes '补充第二天行程'
```

也可通过环境变量 `STACKNEST_DEPLOY_TOKEN` 注入凭据，适合 CI 的 Secret。`--url` 可覆盖 URL 环境变量。远程连接必须使用 HTTPS；SSH 隧道可用 `http://localhost:18080`。

标准输出是 JSON，进度在标准错误输出：

```json
{
  "projectId": "prj-example",
  "slug": "travel",
  "draftId": "dep-new-version",
  "expectedId": "dep-current-version",
  "previewUrl": "https://预览地址/",
  "siteUrl": "https://stack-nest-travel.example.com/",
  "status": "preview"
}
```

文件格式和容量见 [静态站点](README.md#静态站点)。**预览链接默认公开。** 每项目保留一个草稿，有草稿时可在后台发布或丢弃，或者按下节明确替换。

### 更新已有待发布版

用 `--replace-draft` 指定要替换的草稿：

```bash
./stacknest-deploy upload --file ./guide.html --token-file "$HOME/.config/stacknest/travel.token" --replace-draft dep-要替换的草稿编号 --notes '调整第二天行程'
```

草稿编号来自预览回执，也可用 `status` 核对。校验成功后才替换，旧预览地址随即失效。断线后用原文件和原参数续传，发布时使用**新回执**的编号。

## 4. 确认发布

可以直接在后台“版本”页确认。使用具备发布权限的 Token 时，也可以执行：

```bash
./stacknest-deploy publish --token-file "$HOME/.config/stacknest/travel.token" --draft-id dep-new-version --expected-id dep-current-version
```

两个编号都来自上一步返回值；首次正式发布的 `expectedId` 为空时，参数填 `--expected-id none`。不要让脚本重新查询“最新草稿”后直接发布，否则可能发布到别人的新修改。

版本已变化时返回 409，不覆盖新内容。重复确认同一个已正式发布的版本会返回成功。正式站点 URL 保持不变，继续保留当前版与上一版。

## 断点续传与重试

失败后用**同一文件、Token 和参数**重跑。CLI 从服务端确认的位置继续，临时上传保留 24 小时。当前目录的 `.stacknest-upload-文件标识.json` 保存续传状态和成功回执，重复运行会返回同一次上传结果。

指定固定 `--state` 时，部署新文件请换一个状态文件。记录过期、草稿已丢弃或确实要重新部署同一文件时，可删除对应的状态记录。把状态和私有配置排除在 Git 之外：

```gitignore
.stacknest-upload*.json
*.token.local
.env.stacknest.local
```

## GitHub Actions

复制 [示例工作流](../../examples/github-actions/deploy-static.yml)到**你的站点源码仓库**的 `.github/workflows/deploy-static.yml`。它不是 StackNest 自身的镜像发布工作流。

在该仓库的 **Settings → Secrets and variables → Actions** 配置：

| 类型 | 名称 | 内容 |
| --- | --- | --- |
| Variable | `STACKNEST_URL` | 管理端 HTTPS 地址 |
| Variable | `STACKNEST_CLI_VERSION` | 确切的 CLI 版本号，例如 `v1.0.2` |
| Secret | `STACKNEST_DEPLOY_TOKEN` | 本项目的部署 Token |

将示例中的 `site.html` 改成实际文件。Vite 等项目需要在上传前增加自己的构建步骤，再把 `dist` **目录内的内容**打包成 ZIP；平台不代为构建源码。

推送 `main` 默认只上传预览，链接出现在 Actions 的运行摘要中。手动运行工作流并勾选 `publish`，才会上传并正式发布本次版本；Token 也必须有发布权限。同一工作流串行执行，不会自动覆盖已有草稿。已有预览可直接在后台确认，手动重跑工作流不会跳过这项保护。

GitHub 托管 Runner 必须能访问管理端地址。模板不在 Pull Request 事件中使用部署 Secret；不要把 Secret 暴露给外部贡献者提交的代码。失败的整次工作流重跑会使用新的临时状态文件；如果上次已经生成草稿，先到后台核实，不要自动删除。

## 让 AI 使用

从 **项目详情 → 设置 → 自动部署 → 交给 AI 部署** 下载 [stacknest-deploy Skill](../../skills/stacknest-deploy/SKILL.md)，把整个目录放入 AI 工具的 Skill 目录后重载。复制页面上的部署说明给 AI，再提供上传文件和私有 Token 文件路径。

首次使用会补齐缺少的连接信息，也可让 AI 安装 CLI。项目由 Token 自动识别。默认先生成预览，确认发布时沿用该次预览的回执。

```text
使用 $stacknest-deploy 上传当前 HTML，给我预览。
使用 $stacknest-deploy 修改刚才的预览，再给我新链接。
使用 $stacknest-deploy 发布刚才确认的预览。
```

可让 AI 在私有配置中只记住路径或要求仅本次使用。接口见 [自动部署 API](../../openapi/automation.yaml)。
