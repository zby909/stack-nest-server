"""用假 Docker 验证备份控制流，绝不操作真实容器或数据库。"""
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest

if sys.platform.startswith("linux"):
    import fcntl


def main(backup_script=None, status_script=None):
    """模拟成功、数据库失败和恢复 API 失败，验证状态和原备份保留。"""
    if backup_script is not None and status_script is not None:
        backup_script, status_script = Path(backup_script), Path(status_script)
    else:
        backup_script = Path(__file__).with_name("backup.sh")
        status_script = Path(__file__).with_name("backup_status.py")
    with tempfile.TemporaryDirectory(prefix="stacknest-backup-smoke-", dir="/tmp") as temporary:
        root = Path(temporary)
        scripts, binary = root / "scripts", root / "bin"
        scripts.mkdir()
        binary.mkdir()
        (scripts / "backup.sh").write_bytes(backup_script.read_bytes().replace(b"\r\n", b"\n"))
        (scripts / "backup_status.py").write_bytes(status_script.read_bytes())
        compose = root / "compose.yaml"
        compose.write_text("services: {}\n")
        files = root / "files.tar.gz"
        with tarfile.open(files, "w:gz") as archive:
            entry = tarfile.TarInfo("sites/demo/index.html")
            content = b"<!doctype html><h1>fake backup</h1>"
            entry.size = len(content)
            archive.addfile(entry, io.BytesIO(content))
        docker = binary / "docker"
        docker.write_text('''#!/usr/bin/env python3
import json
import os
from pathlib import Path
import sys
args = sys.argv[1:]
with open(os.environ['MOCK_LOG'], 'a') as log: log.write(' '.join(args) + '\\n')
failure = os.environ.get('MOCK_FAIL')
if args[0] == 'inspect': print('true')
elif 'config' in args and '--services' in args: print('postgres\\nstacknest\\nrunner')
elif 'apps-freeze' in args and failure == 'freeze': sys.exit(44)
elif 'apps-thaw' in args and failure == 'thaw': sys.exit(46)
elif 'config' in args and 'json' in args: print(json.dumps({'name': os.environ['MOCK_PROJECT']}))
elif 'ps' in args: print('mock-container')
elif 'pg_dump' in args:
    if failure == 'database': sys.exit(42)
    sys.stdout.buffer.write(b'fake-pg-dump')
elif 'pg_restore' in args: sys.stdin.buffer.read()
elif 'run' in args: sys.stdout.buffer.write(Path(os.environ['MOCK_FILES']).read_bytes())
elif 'start' in args and '--wait' in args: sys.exit(45)
elif 'up' in args and failure == 'restart': sys.exit(43)
''')
        docker.chmod(0o755)
        backups, status = root / "backups", root / "status"
        log = root / "docker.log"
        env = {**os.environ, "PATH": str(binary) + ":" + os.environ["PATH"], "MOCK_LOG": str(log), "MOCK_FILES": str(files), "STACKNEST_BACKUP_KEEP": "1"}
        env["MOCK_PROJECT"] = "stacknest-backup-test-" + str(os.getpid())
        env.pop("STACKNEST_COMPOSE_PROJECT", None)
        env.pop("STACKNEST_BACKUP_LOCK_FD", None)
        command = ["bash", str(scripts / "backup.sh"), str(compose), str(backups), str(status)]
        success = subprocess.run(command, env=env, capture_output=True, text=True)
        assert success.returncode == 0, success.stderr
        record = json.loads((status / "status.json").read_text())
        assert record["state"] == "succeeded" and record["retainedCount"] == 1
        last = record["lastSuccess"]
        calls = log.read_text().splitlines()
        freeze = next(i for i, call in enumerate(calls) if "apps-freeze" in call)
        dump = next(i for i, call in enumerate(calls) if "pg_dump" in call)
        thaw = next(i for i, call in enumerate(calls) if "apps-thaw" in call)
        start = next(i for i, call in enumerate(calls) if "up -d --no-deps --no-recreate --wait --wait-timeout 90 stacknest" in call)
        assert freeze < dump < thaw < start
        assert last["databaseBytes"] == len(b"fake-pg-dump") and last["filesBytes"] == files.stat().st_size
        assert not (status / "status.json").stat().st_mode & 0o022
        failed = subprocess.run(command, env={**env, "MOCK_FAIL": "database"}, capture_output=True, text=True)
        assert failed.returncode != 0
        record = json.loads((status / "status.json").read_text())
        assert record["state"] == "failed" and record["lastAttempt"]["stage"] == "database"
        assert record["lastSuccess"] == last and record["retainedCount"] == 1
        assert "up -d --no-deps --no-recreate --wait --wait-timeout 90 stacknest" in log.read_text().splitlines()[-1]
        assert len(list(backups.glob(".partial-*"))) == 1
        before = len(log.read_text())
        freeze_failed = subprocess.run(command, env={**env, "MOCK_FAIL": "freeze"}, capture_output=True, text=True)
        assert freeze_failed.returncode != 0
        calls = log.read_text()[before:]
        assert "apps-thaw" in calls and "pg_dump" not in calls
        record = json.loads((status / "status.json").read_text())
        assert record["state"] == "failed" and record["lastAttempt"]["stage"] == "apps-freeze"
        assert record["lastSuccess"] == last and record["retainedCount"] == 1
        before = len(log.read_text())
        thaw_failed = subprocess.run(command, env={**env, "MOCK_FAIL": "thaw"}, capture_output=True, text=True)
        assert thaw_failed.returncode != 0
        record = json.loads((status / "status.json").read_text())
        assert record["state"] == "failed" and record["lastAttempt"]["stage"] == "apps-restore"
        assert record["lastSuccess"] and record["retainedCount"] == 1
        calls = log.read_text()[before:].splitlines()
        assert "apps-thaw" in calls[-2] and "up -d --no-deps --no-recreate --wait" in calls[-1]
        restart = subprocess.run(command, env={**env, "MOCK_FAIL": "restart"}, capture_output=True, text=True)
        assert restart.returncode != 0
        record = json.loads((status / "status.json").read_text())
        assert record["state"] == "failed" and record["lastAttempt"]["stage"] == "restart"
        assert record["lastSuccess"] and record["retainedCount"] == 1
        assert len(list(backups.glob("stacknest-*"))) == 1
        # 不同备份目录也共享实例锁；导出进程传入同一描述符时允许嵌套备份。
        lock_path = Path("/run/lock") / ("stacknest-backup-" + env["MOCK_PROJECT"] + ".lock")
        try:
            with lock_path.open("a") as lock:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                before = log.read_text()
                busy_command = command[:3] + [str(root / "another-backup"), str(root / "another-status")]
                busy = subprocess.run(busy_command, env=env, capture_output=True, text=True)
                assert busy.returncode != 0 and "another backup or migration" in busy.stderr
                assert "stop" not in log.read_text()[len(before):]
                inherited = subprocess.run(command, env={**env, "STACKNEST_BACKUP_LOCK_FD": str(lock.fileno())}, pass_fds=(lock.fileno(),), capture_output=True, text=True)
                assert inherited.returncode == 0, inherited.stderr
            print("backup_cross_directory_mutex_and_inherited_lock_passed=true")
        finally:
            lock_path.unlink(missing_ok=True)
        print("backup_helpers_success_database_failure_restart_failure_retention_passed=true")


@unittest.skipUnless(sys.platform.startswith("linux"), "backup shell and flock checks require Linux")
class BackupTests(unittest.TestCase):
    # test_control_flow 将已有假 Docker 演练纳入统一发现，避免只导入脚本却没有执行。
    def test_control_flow(self):
        main()


if __name__ == "__main__":
    main(*sys.argv[1:])
