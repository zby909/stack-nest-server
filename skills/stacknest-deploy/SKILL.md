---
name: stacknest-deploy
description: "使用 StackNest 部署静态网站或更新已有 Docker 应用的镜像，支持连接配置、预览、发布、续传和部署结果查询。用户要求使用本 Skill、部署到 StackNest 或接入其自动部署流程时使用；不用于安装平台或创建 Docker 应用。"
---

# StackNest 部署

使用 `stacknest-deploy` CLI；它负责上传校验、任务提交和有限重试，无需重写 HTTP 协议。

## 选择流程

- HTML / HTM / ZIP / RAR、静态构建产物：读 [静态站点](references/static.md)。默认生成预览；用户明确要求上线时发布该次确切版本，不重复确认。
- 已有 Docker 应用的镜像更新或部署状态：读 [Docker 应用](references/docker.md)。`app deploy` 更新正式应用，没有静态站点式预览；查询或配置请求不授权部署。
- 仅配置客户端：验证连接和绑定目标后结束，不上传或部署。类型未知且已有 Token 时，可用只读 `status`、`app status` 识别；两个都拒绝认证时修复凭据，不猜目标。

## 连接与本机配置

1. 读取 [本机配置](references/local-config.md)。优先使用本次指定信息、用户项目的明确配置，再用私有默认值。依次检查已配置的 CLI、PATH 和下方用户级默认位置；复用兼容工具，不遍历电脑和其他项目寻找凭据。
2. 用 `version`、`--help` 检查兼容性：静态上传需 CLI / 后端 `v1.0.0-rc.2+`，替换草稿需 `v1.0.0-rc.4+`，Docker 需 `v1.0.0-rc.10+` 且 CLI 有 `app` 命令。
3. 只合并询问仍缺少的管理端地址和对应 Token 文件路径。CLI 缺失时按下方规则安装，无需用户提供安装路径。已有 `STACKNEST_URL`、`STACKNEST_DEPLOY_TOKEN` 可直接使用，不打印凭据值。**不用索要项目名称或 slug**；目标由 Token 查询识别。
4. 静态执行 `status`，读取 `id / slug / siteUrl`；Docker 执行 `app status`，读取 `appId / slug / url`。展示识别结果即可；只有与用户明确目标或保存的身份不符、或多个凭据无法选择时才澄清。Token 只绑定一个已有资源，静态和应用凭据不能混用。
5. 配置验证后，在私有安装副本的 `references/local-config.md` 或用户指定私有位置，记录 CLI 路径、URL、资源类型、ID、slug、访问地址和 Token 文件路径。保留其他资源记录，不保存 Token 原文。受 Git 跟踪的模板不写本机配置；没有可写私有位置或用户要求仅本次使用时不保存，也不阻断已授权操作。

尚无 Token 时，引导用户在后台创建：静态在“项目详情 → 设置 → 自动部署”，需要正式发布时授予发布权限；Docker 在“应用详情 → 自动部署”，它可直接更新正式应用。只接收 Token 文件路径，不让用户把内容发进聊天。

## CLI 安装位置

用户指定的位置优先，已有兼容工具继续复用。首次安装或需要另装兼容版本时，默认使用以下用户级位置，供不同项目共用：

| 系统 | 默认可执行文件 |
| --- | --- |
| Windows | `%LOCALAPPDATA%\StackNest\bin\stacknest-deploy.exe` |
| macOS | `~/Library/Application Support/StackNest/bin/stacknest-deploy` |
| Linux | `${XDG_DATA_HOME:-$HOME/.local/share}/stacknest/bin/stacknest-deploy` |

展开环境变量和用户目录后使用绝对路径。识别系统与架构，从[官方发布页](https://github.com/zby909/stack-nest-server/releases)下载兼容版本到临时目录，校验配套 SHA-256 并运行 `version` 核对后，再安装到目标位置；保留 LICENSE / NOTICE，Unix 下设置可执行权限。安装失败保留原工具，不自动迁移或删除旧路径。

将实际 CLI 路径记入私有配置，后续直接复用；不需要修改 PATH 或管理员权限。默认位置不可写时再询问替代位置，不退回每个项目各装一份。

## 执行与交付

- 凭据交由 CLI 从 `--token-file` 或已有环境变量读取，不拼入命令行、写进 Skill、源码、Git 或输出。远程使用 HTTPS，HTTP 仅用于本机回环隧道。
- 参数分别传递，检查退出码；标准输出为 JSON，标准错误为进度，分别解析。CLI 已有有限重试，额外恢复按对应流程执行。
- 鉴权、校验、配额和版本冲突时停止；不通过改目标、改基线、丢弃草稿或无限重试强行完成。
- 保存本次回执中的版本或任务代次，后续确认、续传、等待都绑定原目标，不盲目跟随最新任务。
- 访问返回地址，按站点内容或应用实际入口核实。区分“部署已完成”和“外网访问验证未完成”，不因访问验证失败再次部署。
- 返回资源、实际状态、链接和必要编号。静态预览默认公开；Docker 失败恢复容器不等于撤销业务数据变化。
