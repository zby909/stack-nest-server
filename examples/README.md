# 第一次部署

先按 [Docker 部署](../docs/INSTALL_RELEASE.md)启动平台，再用这个最小站点体验一次发布。

## 静态站点

下载 [hello-stacknest.zip](hello-stacknest.zip)，在后台“部署静态项目”中填写名称并上传。打开待发布版本的预览地址，确认后点“发布此版本”。

[源码](static-site/index.html)只有一个 HTML 文件，没有外部依赖。修改文案后在本目录打包：

```bash
python3 -m zipfile -c hello-stacknest.zip static-site/index.html
```

Windows 也可以把 `static-site/index.html` 压缩为 ZIP。上传新包，预览后发布；版本页可退回上一正式版。

## Docker 应用

平台接收已构建的 Linux amd64 镜像。填写自己可拉取的镜像地址、容器 HTTP 端口和必要环境变量，启动后通过应用域名访问。

需要最小 HTTP 镜像验证时，可复用仓库中的 [smoke-http](../deploy/smoke-http/Dockerfile)。它监听 8080、使用非 root 用户，包含测试接口。构建与推送到你自己的仓库后，再填入平台：

```bash
# 在 server/ 下执行，将 YOUR_REGISTRY 替换为自己的镜像仓库。
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o deploy/smoke-http/smoke-http ./deploy/smoke-http
docker build --platform linux/amd64 -t YOUR_REGISTRY/stacknest-demo:v1 deploy/smoke-http
docker push YOUR_REGISTRY/stacknest-demo:v1
```

[应用配置说明](../docs/DOCKER_APPS.md)
