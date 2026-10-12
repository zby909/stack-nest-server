#!/usr/bin/env python3
"""在隔离 Docker 网络验证实际 Nginx 入口，不读写现有平台数据。"""
import argparse
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import shutil
import ssl
import subprocess
import tempfile
import threading
import time
import uuid


# run 隐藏 Docker 命令正文；失败只报告动作和状态码。
def run(args, stage="docker"):
    result = subprocess.run(args, capture_output=True, text=True, timeout=45)
    if result.returncode:
        raise RuntimeError(stage + " failed (exit " + str(result.returncode) + ")")
    return result.stdout.strip()


class Backend(BaseHTTPRequestHandler):
    # do_GET 用受控响应确认代理的主机、协议及 WebSocket 标头。
    def do_GET(self):
        api = self.path.startswith("/api/")
        status = 401 if api and self.path != "/api/health" else 200
        body = json.dumps({
            "fixture": "isolated-backend",
            "host": self.headers.get("Host"),
            "proto": self.headers.get("X-Forwarded-Proto"),
            "upgrade": self.headers.get("Upgrade"),
            "connection": self.headers.get("Connection"),
        }).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    # log_message 禁止记录访问内容或污染回归输出。
    def log_message(self, *args):
        pass


# request 使用真实 HTTP / TLS 请求，HTTPS 校验证书和管理域名。
def request(port, host, path="/", headers=None, context=None):
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    conn.connect()
    if context:
        conn.sock = context.wrap_socket(conn.sock, server_hostname="stack-nest.example.com")
    fields = {"Host": host}
    fields.update(headers or {})
    try:
        conn.request("GET", path, headers=fields)
        response = conn.getresponse()
        return response.status, dict(response.getheaders()), response.read()
    finally:
        conn.close()


# check_routes 验证真实静态缓存、暂停、未知域名、SPA 及后端代理。
def check_routes(port, context=None, cloudflare=False, ssh=False):
    fields = {"X-Forwarded-Proto": "https"} if cloudflare else {}
    admin = "localhost:" + str(port) if ssh else "stack-nest.example.com"
    suffix = "localhost" if ssh else "example.com"
    count = 0

    # expect 统一检查状态并计数，只在失败时报告测试主机与路径。
    def expect(host, path, status, extra=None):
        nonlocal count
        headers = dict(fields)
        headers.update(extra or {})
        result = request(port, host, path, headers, context)
        assert result[0] == status, (host, path, result[0], status)
        count += 1
        return result

    for path in ["/", "/login", "/apps/test/logs", "/projects/test/versions"]:
        status, headers, body = expect(admin, path, 200)
        assert b'<div id="root">' in body or b"<div id='root'>" in body, path
    expect(admin, "/missing.css", 404)
    expect(admin, "/api/projects", 401)
    info = json.loads(expect(admin, "/api/health", 200)[2])
    assert info["host"] == admin
    assert info["proto"] == ("https" if context or cloudflare else "http")

    static = "stack-nest-fixture." + suffix
    status, headers, body = expect(static, "/", 200)
    if ssh:
        assert json.loads(body)["fixture"] == "isolated-backend"
    else:
        assert b"static-ingress-fixture" in body
    if not ssh:
        assert headers.get("Cache-Control") == "no-cache"
        etag = headers["ETag"]
        expect(static, "/", 304, {"If-None-Match": etag})
        expect(static, "/", 304, {"If-Modified-Since": headers["Last-Modified"]})
        css = expect(static, "/style.css", 200)
        assert css[1].get("Cache-Control") == "no-cache"
        assert "text/css" in css[1].get("Content-Type", "")
        expect(static, "/profile/1", 200)
        expect(static, "/missing.js", 404)
        expect(static, "/.env", 404)
        expect("stack-nest-paused.example.com", "/", 503)
        expect("stack-nest-fixture.other.example.com", "/", 404)
        expect("unknown.example.com", "/", 404)

    app = "stack-nest--app-fixture." + suffix
    proxy = json.loads(expect(app, "/", 200, {"Upgrade": "websocket", "Connection": "upgrade"})[2])
    assert proxy["host"] == app and proxy["upgrade"] == "websocket"
    assert proxy["connection"].lower() == "upgrade"
    expect("stack-nest--00000000-0000-0000-0000-000000000000." + suffix, "/", 200)
    if ssh:
        expect("127.0.0.1:" + str(port), "/", 200)
        for host in ["unknown.localhost", "unknown.example.com", "stack-nest-fixture.other.localhost",
                     "stack-nest--00000000-0000-0000-0000-000000000000.other.localhost",
                     "stack-nest--00000000-0000-0000-0000-000000000000.invalid"]:
            expect(host, "/", 404)
    return count


# main 用临时站点、证书与独立网络测试四种入口，finally 清理自己的资源。
def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", required=True, help="already built stacknest-web image")
    args = parser.parse_args()
    templates = Path(__file__).resolve().parent / "nginx"
    token = uuid.uuid4().hex[:12]
    network = "stacknest-ingress-check-" + token
    name = "stacknest-ingress-check-" + token
    backend = None
    created = False
    total = 0
    with tempfile.TemporaryDirectory(prefix="stacknest-ingress-", dir="/tmp") as temporary:
        root = Path(temporary)
        root.chmod(0o755)
        data = root / "data"
        (data / "sites/fixture").mkdir(parents=True)
        (data / "paused").mkdir()
        (data / "sites/fixture/index.html").write_text("<!doctype html>static-ingress-fixture", encoding="utf-8")
        (data / "sites/fixture/style.css").write_text("body { color: green; }", encoding="utf-8")
        (data / "sites/fixture/.env").write_text("hidden-fixture", encoding="utf-8")
        (data / "paused/paused").touch()
        cert = root / "cert"
        cert.mkdir()
        run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
             "-subj", "/CN=stack-nest.example.com", "-addext", "subjectAltName=DNS:stack-nest.example.com,DNS:*.example.com",
             "-keyout", str(cert / "private.key"), "-out", str(cert / "certificate.pem")], "temporary certificate")
        context = ssl.create_default_context(cafile=str(cert / "certificate.pem"))
        try:
            run(["docker", "network", "create", "--label", "com.stacknest.check=nginx", network])
            created = True
            config = json.loads(run(["docker", "network", "inspect", network]))[0]
            gateway = config["IPAM"]["Config"][0]["Gateway"]
            backend = ThreadingHTTPServer((gateway, 8080), Backend)
            threading.Thread(target=backend.serve_forever, daemon=True).start()
            for mode, base in [("http", "default.conf.template"), ("tls", "tls.conf.template"),
                               ("cloudflare", "cloudflare.conf.template"), ("tunnel", "default.conf.template")]:
                command = ["docker", "run", "-d", "--name", name, "--network", network,
                           "--label", "com.stacknest.check=nginx", "--add-host", "stacknest:" + gateway,
                           "-e", "STACKNEST_ADMIN_DOMAIN=stack-nest.example.com",
                           "-e", "STACKNEST_BASE_DOMAIN=example.com",
                           "-e", "NGINX_ENVSUBST_FILTER=STACKNEST_ADMIN_DOMAIN|STACKNEST_BASE_DOMAIN",
                           "-v", str(templates / base) + ":/etc/nginx/templates/default.conf.template:ro",
                           "-v", str(data) + ":/data:ro", "-p", "127.0.0.1::80"]
                if mode in ("tls", "cloudflare"):
                    command += ["-p", "127.0.0.1::443", "-v", str(cert) + ":/etc/nginx/certs:ro"]
                if mode == "tunnel":
                    for filename in ["ssh.conf.template", "cloudflare-tunnel.conf.template"]:
                        command += ["-v", str(templates / filename) + ":/etc/nginx/templates/" + filename + ":ro"]
                    command += ["-p", "127.0.0.1::81", "-p", "127.0.0.1::82"]
                command += [args.image]
                run(command, "start isolated nginx")
                try:
                    for attempt in range(40):
                        state = json.loads(run(["docker", "inspect", name]))[0]
                        if state["State"]["Running"]:
                            try:
                                run(["docker", "exec", name, "nginx", "-t"], "nginx syntax")
                                bound = state["NetworkSettings"]["Ports"].get("80/tcp")
                                if not bound:
                                    raise RuntimeError("ports are not ready")
                                request(int(bound[0]["HostPort"]), "stack-nest.example.com")
                                break
                            except (RuntimeError, OSError, http.client.HTTPException):
                                pass
                        time.sleep(0.2)
                    else:
                        raise RuntimeError("isolated nginx did not become ready")
                    ports = {int(key.split("/")[0]): int(value[0]["HostPort"])
                             for key, value in state["NetworkSettings"]["Ports"].items() if value}
                    if mode in ("tls", "cloudflare"):
                        result = request(ports[80], "stack-nest.example.com")
                        assert result[0] == 308
                        total += 1
                        total += check_routes(ports[443], context=context)
                    elif mode == "tunnel":
                        total += check_routes(ports[81], cloudflare=True)
                        total += check_routes(ports[82], ssh=True)
                        assert request(ports[81], "stack-nest.example.com")[0] == 308
                        assert request(ports[81], "demo.example.com", headers={"X-Forwarded-Proto": "https"})[0] == 200
                        total += 2
                    else:
                        total += check_routes(ports[80])
                    print("nginx_mode=" + mode + " verified", flush=True)
                finally:
                    run(["docker", "rm", "-f", name], "clean isolated nginx")
        finally:
            if backend:
                backend.shutdown()
                backend.server_close()
            if created:
                run(["docker", "network", "rm", network], "clean isolated network")
    print("nginx_requests_verified=" + str(total))


if __name__ == "__main__":
    main()
