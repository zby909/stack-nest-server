# 自动部署静态项目

让本机脚本、AI 或 GitHub Actions 上传 HTML / ZIP / RAR，返回预览地址；确认后再发布到原项目网址。无需 SSH、登录 Cookie 或服务器密码。

本功能从 `v1.0.0-rc.2` 开始提供，需要配套更新后端和 Web。早期 `v1.0.0-rc.1` 不包含本功能，也没有 CLI 下载包。

## 1. 创建项目凭据

先在平台创建一个静态项目，然后进入 **项目详情 → 设置 → 自动部署 → 创建部署 Token**。

- 默认有效期 30 天、**仅预览**。这适合让 AI 上传后由你确认。
- 需要脚本直接上线时，勾选 **允许正式发布**；调用方仍须明确执行发布命令。
- 原文只显示一次，保存在本机私有文件或 GitHub Secret。每个项目最多保留 10 个 Token，不用的或过期的可以撤销。

一个 Token 绑定一个已有项目。它不能创建或删除项目、管理邮箱、操作 Docker 应用，也不能访问其他项目或接管其他 Token / 浏览器的上传。项目删除、Token 撤销或过期、创建者删除或失去权限后，后续请求被拒绝。项目归属与签发时不一致也会失效。已开始处理的发布可能继续完成。

## 2. 准备命令行工具

[Releases](https://github.com/zby909/stack-nest-server/releases) 已提供 Linux、Windows、macOS 的 amd64 / arm64 CLI 与 `.sha256` 校验文件。选择 `v1.0.0-rc.2` 或更新版本，下载匹配本机的 `stacknest-deploy-版本-系统-架构`，校验后解压即可，无需 Go 或 Docker。

从源码构建则在 `server/` 执行（Go 版本见 `go.mod`）：

```bash
go build -o stacknest-deploy ./cmd/stacknest-deploy
./stacknest-deploy --help
```

Windows 使用 `go build -o stacknest-deploy.exe ./cmd/stacknest-deploy`。

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

单个 HTML / HTM 的名称不限，会作为 `index.html` 发布；配套资源须内嵌或使用外部 URL。ZIP / RAR 的内容仍须包含 `index.html`。文件不超过 90 MiB，沿用平台原有解压和磁盘检查。

**预览默认公开，持有地址即可访问。** 上传预览不会更新正式站点。项目同时只保留一个草稿；普通 `upload` 遇到已有草稿会明确失败，不自动覆盖。可按下一节更新指定草稿，或先在后台发布、丢弃。

### 更新已有待发布版

后端和 CLI 均升级至 `v1.0.0-rc.3` 或更新版本后，可明确替换某个草稿：

```bash
./stacknest-deploy upload --file ./guide.html --token-file "$HOME/.config/stacknest/travel.token" --replace-draft dep-要替换的草稿编号 --notes '调整第二天行程'
```

旧编号来自要修改的预览回执，也可通过 `status` 核实。新文件校验成功后才替换，失败保留旧草稿；成功返回新的 `draftId` 和 `previewUrl`，旧预览地址失效。正式站点保持原样。上传期间正式版或指定草稿发生变化时返回冲突，不自动改为覆盖最新草稿。普通 `upload` 仍禁止覆盖。

断线后保留原文件和 `--replace-draft` 参数重跑，续传记录绑定这次替换目标。之后确认发布时使用**新回执**的编号。网页可在项目“版本”页点击“更新待发布版”。

## 4. 确认发布

可以直接在后台“版本”页确认。使用具备发布权限的 Token 时，也可以执行：

```bash
./stacknest-deploy publish --token-file "$HOME/.config/stacknest/travel.token" --draft-id dep-new-version --expected-id dep-current-version
```

两个编号都来自上一步返回值；首次正式发布的 `expectedId` 为空时，参数填 `--expected-id none`。不要让脚本重新查询“最新草稿”后直接发布，否则可能发布到别人的新修改。

版本已变化时返回 409，不覆盖新内容。重复确认同一个已正式发布的版本会返回成功。正式站点 URL 保持不变，继续保留当前版与上一版。

## 断点续传与重试

默认在当前目录写 `.stacknest-upload-文件标识.json`，文件名由目标、文件、参数及凭据的摘要决定。新文件自动使用新记录，只保存上传编号、文件摘要、目标、版本和 Token 指纹，不保存凭据原文。失败后使用**同一文件和同一组参数**重跑，会从服务端确认偏移继续，临时上传保留 24 小时。

连续失败最多尝试四次，不会无限重试。同一续传记录的文件内容、Token 或参数改变时拒绝继续；上传过程中不要修改本地文件。成功后的状态文件作为幂等收据保留，重复运行仍确认同一次上传，不生成新版本。

如果手动指定了固定的 `--state`，下一次文件更新要换一个状态文件。记录过期、版本被丢弃，或确实需要重新部署相同内容时，可以删除对应的状态文件再执行。旧收据可按需清理。不要把状态文件、Token 或本机配置提交到 Git；项目 `.gitignore` 建议加入：

```gitignore
.stacknest-upload*.json
*.token.local
.env.stacknest.local
```

## GitHub Actions

复制 [示例工作流](../examples/github-actions/deploy-static.yml)到**你的站点源码仓库**的 `.github/workflows/deploy-static.yml`。它不是 StackNest 自身的镜像发布工作流。

在该仓库的 **Settings → Secrets and variables → Actions** 配置：

| 类型 | 名称 | 内容 |
| --- | --- | --- |
| Variable | `STACKNEST_URL` | 管理端 HTTPS 地址 |
| Variable | `STACKNEST_CLI_VERSION` | 包含 CLI 下载包的确切版本号，例如 `v1.0.0-rc.2` |
| Secret | `STACKNEST_DEPLOY_TOKEN` | 本项目的部署 Token |

将示例中的 `site.html` 改成实际文件。Vite 等项目需要在上传前增加自己的构建步骤，再把 `dist` **目录内的内容**打包成 ZIP；平台不代为构建源码。

推送 `main` 默认只上传预览，链接出现在 Actions 的运行摘要中。手动运行工作流并勾选 `publish`，才会上传并正式发布本次版本；Token 也必须有发布权限。同一工作流串行执行，不会自动覆盖已有草稿。已有预览可直接在后台确认，手动重跑工作流不会跳过这项保护。

GitHub 托管 Runner 必须能访问管理端地址。模板不在 Pull Request 事件中使用部署 Secret；不要把 Secret 暴露给外部贡献者提交的代码。失败的整次工作流重跑会使用新的临时状态文件；如果上次已经生成草稿，先到后台核实，不要自动删除。

## 让 AI 使用

进入 **项目详情 → 设置 → 自动部署 → 交给 AI 部署**，可下载通用 Skill、打开 CLI 发布页，并复制已填入当前后台地址和项目标识的说明。将说明交给 AI，再指定要上传的文件和私有 Token 文件路径即可；复制内容和 Skill 下载包均不包含凭据。默认只生成预览。

可以把下面的说明放进旅游攻略等**用户项目**的 `AGENTS.md`，只写路径和规则：

> 修改完成后，调用 stacknest-deploy 上传单个 HTML 并返回预览链接。管理端地址为我的 StackNest URL，凭据从指定的私有 Token 文件读取，不输出其内容。使用默认状态文件，失败时用相同文件和参数续传。默认只生成预览，只有我明确确认某个预览后，才允许发布该确切版本。不得擅自覆盖或丢弃已有草稿。

之后可以直接说“改完行程，给我一个新预览”。

也可以使用仓库附带的 [stacknest-deploy Skill](../skills/stacknest-deploy/SKILL.md)。将 `skills/stacknest-deploy` 整个目录复制到你的 AI 工具支持的 Skill 目录；Codex 默认使用 `~/.codex/skills/`（配置了 `CODEX_HOME` 时使用其中的 `skills/`）。首次使用时，AI 会先检查现有配置和 PATH，只询问缺少的管理端地址、CLI 路径和 Token 文件路径；项目由 Token 自动查询识别，不用填写项目名或 slug。未安装 CLI 可以让它协助下载。不需要先手动编辑配置文件。重新加载 Skill 后可以这样说：

```text
使用 $stacknest-deploy 上传当前 HTML，给我预览。
使用 $stacknest-deploy 将当前 HTML 直接发布到我指定的项目，返回正式链接。
```

连接验证通过后，AI 会在私有安装副本的 `references/local-config.md` 记住路径、地址和项目对应关系，后续复用；不保存 Token 原文，也不修改仓库里的通用模板。换电脑或凭据过期时只补充失效项。可以要求仅本次使用、不保存配置。

Skill 默认生成预览；明确要求正式上线时，会使用本次上传返回的版本编号发布，不重复询问。已有其他草稿或版本冲突时停止。它复用 CLI，不需要另外安装上传脚本。

需要继续修改预览时，可以说“使用 $stacknest-deploy 修改刚才的预览，再给我新链接”。Skill 会使用该次回执的旧草稿编号替换，成功后保留新回执供发布使用；不会替换未指定的其他草稿。

协议见 [自动部署 API](../openapi/automation.yaml)。Bearer 只在 `/api/deploy/v1/` 生效；正常管理 API 继续使用会话 Cookie 和同源校验。文件分片、校验、预览和发布复用现有业务流程，数据库只保存 Token 的 SHA-256 摘要。
