#!/usr/bin/env python3
"""在隔离容器中验证真实浏览器发布链路，邮件只发到临时 TLS 收件箱。"""
import argparse
import http.client
import json
import os
import shutil
from pathlib import Path
import subprocess
import sys
import tempfile
import uuid
import zipfile


# run 检查真实命令结果，失败只暴露阶段和退出码。
def run(args, env, stage, timeout=180, cwd=None):
    result = subprocess.run(args, env=env, cwd=cwd, capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        print(result.stdout[-5000:], file=sys.stderr)
        print(result.stderr[-3000:], file=sys.stderr)
        raise RuntimeError(stage + " failed (exit " + str(result.returncode) + ")")
    return result.stdout


# browser_check 从真实界面走完整发布链路；只传递本次隔离实例的地址和虚构归档。
def browser_check(root, work, port, mailbox_port, env):
    web = root.parent / "web"
    pnpm = shutil.which("pnpm.cmd") or shutil.which("pnpm")
    if not pnpm or not (web / "scripts/release.config.ts").is_file():
        raise RuntimeError("Release browser check requires the matching web checkout and pnpm")
    browser_env = dict(env, STACKNEST_SMOKE_BASE_URL="http://localhost:" + str(port),
                       STACKNEST_SMOKE_MAILBOX_URL="http://127.0.0.1:" + str(mailbox_port))
    for version, color in ((1, "200,10,20"), (2, "10,20,200")):
        archive = work / ("version-" + str(version) + ".zip")
        with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as target:
            target.writestr("index.html", '<!doctype html><html><head><link rel="stylesheet" href="/style.css"></head>'
                            '<body><h1>Release version ' + str(version) + '</h1><script src="/main.js"></script></body></html>')
            target.writestr("style.css", "h1 { color: rgb(" + color + "); }")
            target.writestr("main.js", 'document.body.dataset.version = "' + str(version) + '";')
            # 首版超过一个 4 MiB 分片，使真实浏览器执行多次分片请求。
            if version == 1:
                target.writestr("padding.bin", bytes(5 * 1024 * 1024), compress_type=zipfile.ZIP_STORED)
        browser_env["STACKNEST_SMOKE_ZIP_" + str(version)] = str(archive)
    output = run([pnpm, "exec", "playwright", "test", "--config", "scripts/release.config.ts"],
                 browser_env, "real browser release lifecycle", 180, cwd=web)
    print(output[-2000:])


# request 验证通过实际 Nginx 入口的响应，不使用环境代理。
def request(port, host, path):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
    try:
        connection.request("GET", path, headers={"Host": host})
        response = connection.getresponse()
        return response.status, response.read()
    finally:
        connection.close()


# check_release 从空数据库执行迁移并启动平台，只清理本次唯一项目的测试资源。
def check_release(root, prefix, version):
    project = "stacknest-release-check-" + uuid.uuid4().hex[:12]
    env = {key: value for key, value in os.environ.items() if not key.startswith(("STACKNEST_", "COMPOSE_"))}
    values = {
        "COMPOSE_PROJECT_NAME": project, "STACKNEST_RELEASE": version,
        "STACKNEST_ADMIN_DOMAIN": "stack-nest.example.com", "STACKNEST_BASE_DOMAIN": "example.com",
        "STACKNEST_POSTGRES_PASSWORD": "release_check_only",
        "STACKNEST_DATABASE_URL": "postgres://stacknest:release_check_only@postgres:5432/stacknest?sslmode=disable",
        "STACKNEST_AUTH_ADMIN_EMAIL": "admin@example.com",
        "STACKNEST_AUTH_SECRET": "release-check-not-a-real-secret-" + uuid.uuid4().hex,
        "STACKNEST_SMTP_HOST": "release-mail", "STACKNEST_SMTP_PORT": "2465",
        "STACKNEST_SMTP_USER": "smtp@example.com", "STACKNEST_SMTP_PASSWORD": "release-mail-only",
    }
    values["STACKNEST_DOCKER_STORAGE_DIR"] = run(
        ["docker", "info", "--format", "{{.DockerRootDir}}"], env, "Docker storage directory").strip()
    for component in ("app", "web", "migrate"):
        values["STACKNEST_" + component.upper() + "_IMAGE"] = prefix + "/stacknest-" + component + ":" + version
    with tempfile.TemporaryDirectory(prefix=project + "-") as work:
        work = Path(work)
        (work / "backup-status").mkdir()
        values["STACKNEST_BACKUP_STATUS_DIR"] = (work / "backup-status").as_posix()
        env.update(values)
        settings = work / "test.env"
        settings.write_text("\n".join(key + "=" + value for key, value in values.items()) + "\n", encoding="utf-8")
        override = work / "ports.yaml"
        mailbox_image = project + "-mail:local"
        override.write_text('''services:
  nginx:
    ports: !override
      - "127.0.0.1:0:82"
  stacknest:
    environment:
      SSL_CERT_FILE: /run/release-mail/ca.pem
    volumes:
      - release-mail-certs:/run/release-mail:ro
    depends_on:
      release-mail:
        condition: service_healthy
  release-mail:
    image: ''' + mailbox_image + '''
    mem_limit: 64m
    cpus: 0.25
    pids_limit: 32
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    read_only: true
    ports:
      - "127.0.0.1:0:8025"
    volumes:
      - release-mail-certs:/certs
    healthcheck:
      test: ["CMD", "/mailbox", "health"]
      interval: 2s
      timeout: 3s
      retries: 15
volumes:
  release-mail-certs:
''', encoding="utf-8")
        compose = ["docker", "compose", "--env-file", str(settings), "-p", project,
                   "-f", str(root / "compose.release.yaml"), "-f", str(root / "compose.ssh.yaml"),
                   "-f", str(override)]
        try:
            run(["docker", "build", "-f", str(root / "deploy/smoke-mail/Dockerfile"), "-t", mailbox_image, str(root)],
                env, "test mailbox build", 300)
            run(compose + ["config", "--quiet"], env, "isolated Compose validation")
            run(compose + ["up", "-d", "--wait", "--wait-timeout", "120", "--pull", "missing"], env, "release startup", 240)
            raw = run(compose + ["port", "nginx", "82"], env, "entry port")
            port = int(raw.strip().rsplit(":", 1)[1])
            status, body = request(port, "localhost", "/projects")
            if status != 200 or b'id="root"' not in body:
                raise RuntimeError("built frontend missing")
            status, body = request(port, "localhost", "/api/health")
            if status != 200 or json.loads(body).get("status") != "ok":
                raise RuntimeError("database-backed API health check failed")
            identity = json.loads(run(compose + ["exec", "-T", "stacknest", "/usr/local/bin/stacknest", "version"], env, "build identity"))
            if identity.get("version") != version or identity.get("revision") in (None, "", "unknown"):
                raise RuntimeError("release build identity is missing or incorrect")
            for path in ("/api/projects", "/api/apps", "/api/users", "/api/version"):
                if request(port, "localhost", path)[0] != 401:
                    raise RuntimeError("anonymous management access was not rejected: " + path)
            if request(port, "unexpected.example.com", "/")[0] != 404:
                raise RuntimeError("unknown Host was not rejected")
            run(compose + ["exec", "-T", "runner", "/usr/local/bin/stacknest", "runner-health"], env, "Runner health")
            run(compose + ["exec", "-T", "nginx", "nginx", "-t"], env, "Nginx syntax")
            migration = run(compose + ["ps", "-a", "-q", "migrate"], env, "migration container").strip()
            state = run(["docker", "inspect", "-f", "{{.State.Status}}:{{.State.ExitCode}}", migration], env, "migration exit").strip()
            if state != "exited:0":
                raise RuntimeError("database migrations did not complete")
            raw = run(compose + ["port", "release-mail", "8025"], env, "test mailbox port")
            browser_check(root, work, port, int(raw.strip().rsplit(":", 1)[1]), env)
            print("Release check passed: real browser login, chunk upload, preview, publish, update and rollback; API, PostgreSQL, SMTP, Nginx and Runner")
        except Exception:
            # 仅本次随机项目的测试日志；这里从不读取真实实例的配置或日志。
            logs = subprocess.run(compose + ["logs", "--no-color", "--tail", "20"],
                                  env=env, capture_output=True, text=True, timeout=30)
            print(logs.stdout[-7000:], file=sys.stderr)
            raise
        finally:
            try:
                run(compose + ["down", "--volumes", "--remove-orphans"], env, "isolated resource cleanup", 120)
            finally:
                subprocess.run(["docker", "image", "rm", mailbox_image], env=env, capture_output=True, timeout=30)


# main 只接收待发布镜像前缀及版本，测试数据不来自真实实例。
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image-prefix", required=True)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()
    check_release(Path(__file__).resolve().parents[1], args.image_prefix, args.version)


if __name__ == "__main__":
    main()
