#!/usr/bin/env python3
"""用独立项目、数据卷和随机回环端口验证三个待发布镜像，不发送邮件。"""
import argparse
import http.client
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import uuid


# run 检查真实命令结果，失败只暴露阶段和退出码。
def run(args, env, stage, timeout=180):
    result = subprocess.run(args, env=env, capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        print(result.stderr[-3000:], file=sys.stderr)
        raise RuntimeError(stage + " failed (exit " + str(result.returncode) + ")")
    return result.stdout


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
        "STACKNEST_SMTP_HOST": "localhost", "STACKNEST_SMTP_PORT": "465",
        "STACKNEST_SMTP_USER": "smtp@example.com", "STACKNEST_SMTP_PASSWORD": "not-used",
    }
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
        override.write_text('services:\n  nginx:\n    ports: !override\n      - "127.0.0.1:0:82"\n', encoding="utf-8")
        compose = ["docker", "compose", "--env-file", str(settings), "-p", project,
                   "-f", str(root / "compose.release.yaml"), "-f", str(root / "compose.ssh.yaml"),
                   "-f", str(override)]
        try:
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
            for path in ("/api/projects", "/api/apps", "/api/users"):
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
            print("Release smoke check passed: frontend, PostgreSQL migrations, API, Runner, authentication and Host routing")
        except Exception:
            # 仅本次随机项目的测试日志；这里从不读取真实实例的配置或日志。
            logs = subprocess.run(compose + ["logs", "--no-color", "--tail", "20"],
                                  env=env, capture_output=True, text=True, timeout=30)
            print(logs.stdout[-7000:], file=sys.stderr)
            raise
        finally:
            run(compose + ["down", "--volumes", "--remove-orphans"], env, "isolated resource cleanup", 120)


# main 只接收待发布镜像前缀及版本，测试数据不来自真实实例。
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image-prefix", required=True)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()
    check_release(Path(__file__).resolve().parents[1], args.image_prefix, args.version)


if __name__ == "__main__":
    main()
