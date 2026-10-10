# 测试二进制在开发机交叉编译，服务器无需拉取构建工具。
FROM scratch
COPY --chmod=755 smoke-http /smoke-http
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/smoke-http"]
