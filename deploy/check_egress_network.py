#!/usr/bin/env python3
"""在独立 Linux 网络/挂载命名空间验证规则，不连接或扫描实际局域网设备。"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time

SERVER = """
import socket,sys
listener=socket.socket(socket.AF_INET6,socket.SOCK_STREAM)
listener.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
listener.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_V6ONLY,0)
listener.bind(("::",int(sys.argv[1])))
listener.listen(16)
while True:
 client,_=listener.accept()
 try:
  client.settimeout(2); client.recv(4096)
  client.sendall(b"HTTP/1.1 200 OK\\r\\nContent-Length: 3\\r\\nConnection: close\\r\\n\\r\\nok\\n")
 except OSError: pass
 finally: client.close()
"""
REQUEST = """
import socket,sys
try:
 with socket.create_connection((sys.argv[1],int(sys.argv[2])),timeout=1.5) as client:
  client.sendall(b"GET / HTTP/1.1\\r\\nHost: test\\r\\nConnection: close\\r\\n\\r\\n")
  data=client.recv(128)
  sys.exit(0 if b"200 OK" in data else 1)
except OSError: sys.exit(1)
"""

# run 在测试命名空间内执行固定参数，失败保留有限的诊断。
def run(args, **kwargs):
    result = subprocess.run(args, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=kwargs.pop("timeout", 20), **kwargs)
    if result.returncode:
        raise RuntimeError(f"{args[0]}: {result.stderr[-1000:]}")
    return result.stdout

# peer 创建独立的虚拟设备，地址只在测试命名空间内可见。
def peer(name, interface, host4, guest4, host6, guest6, bridge=None):
    run(["ip", "netns", "add", name])
    run(["ip", "link", "add", interface, "type", "veth", "peer", "name", "guest"])
    run(["ip", "link", "set", "guest", "netns", name])
    if bridge:
        run(["ip", "link", "set", interface, "master", bridge])
    else:
        run(["ip", "address", "add", host4, "dev", interface])
        run(["ip", "-6", "address", "add", host6, "dev", interface, "nodad"])
    run(["ip", "link", "set", interface, "up"])
    base = ["ip", "-n", name]
    run(base + ["link", "set", "lo", "up"])
    run(base + ["address", "add", guest4, "dev", "guest"])
    run(base + ["-6", "address", "add", guest6, "dev", "guest", "nodad"])
    run(base + ["link", "set", "guest", "up"])
    run(base + ["route", "add", "default", "via", host4.split("/")[0]])
    run(base + ["-6", "route", "add", "default", "via", host6.split("/")[0]])

# request 检查成功及被拒绝的真实 TCP 通信，不只验证规则字符串。
def request(namespace, address, port, expected, label):
    command = (["ip", "netns", "exec", namespace] if namespace else []) + [
        sys.executable, "-c", REQUEST, address, str(port)]
    passed = subprocess.run(command, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5).returncode == 0
    if passed != expected:
        raise AssertionError(f"{label}: expected {expected}, got {passed}")
    print(f"pass={label}", flush=True)

# GoPolicy 通过与生产相同的 Go 程序核验规则，Python 仅创建测试虚拟设备。
class GoPolicy:
    TABLE = "stacknest_egress"
    CONFIG = None
    STATE = None

    # write_json 保存隔离命名空间内的测试配置。
    @staticmethod
    def write_json(path, value):
        path.write_text(json.dumps(value) + "\n")

    # synchronize 实际调用 Go 的原子规则加载器，失败保留上一份规则。
    def synchronize(self, expected=None):
        binary = os.environ.get("STACKNEST_EGRESS_BINARY", "/usr/local/bin/stacknest-egress")
        result = subprocess.run([binary, "sync", "--config", str(self.CONFIG), "--state", str(self.STATE)], text=True, capture_output=True, timeout=20)
        if result.returncode:
            raise ValueError("Go policy rejected configuration")
        return None

    # table_state 读取隔离网络内的实际规则。
    @staticmethod
    def table_state():
        return json.loads(run(["nft", "-j", "list", "table", "inet", GoPolicy.TABLE]))

    # fingerprint 排除实时计数和句柄，确认失败更新没有改变上一份规则。
    @staticmethod
    def fingerprint(value):
        if isinstance(value, dict):
            return {key: GoPolicy.fingerprint(item) for key, item in value.items() if key not in ("metainfo", "handle", "packets", "bytes")}
        if isinstance(value, list):
            return [GoPolicy.fingerprint(item) for item in value if not (isinstance(item, dict) and "metainfo" in item)]
        return value


# control_request 通过真实 Unix socket 访问 Go 服务，测试不使用宿主生产控制目录。
def control_request(socket_path, method, value=None):
    import http.client
    import socket
    raw = json.dumps(value).encode() if value is not None else b""
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
        connection.settimeout(10)
        connection.connect(str(socket_path))
        request = (method + " /config HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: " + str(len(raw)) + "\r\nConnection: close\r\n\r\n").encode() + raw
        connection.sendall(request)
        response = http.client.HTTPResponse(connection)
        response.begin()
        return response.status, json.loads(response.read())

# control_checks 覆盖原子配置、乐观锁、非法输入、权限与服务重启，不访问生产 LAN。
def control_checks(policy, config):
    import concurrent.futures
    import stat
    binary = os.environ.get("STACKNEST_EGRESS_BINARY", "/usr/local/bin/stacknest-egress")
    command = [binary, "watch", "--config", str(policy.CONFIG), "--state", str(policy.STATE)]
    process = subprocess.Popen(command, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    socket_path = policy.STATE / "control.sock"
    try:
        deadline = time.monotonic() + 6
        while not socket_path.exists():
            if process.poll() is not None or time.monotonic() > deadline:
                raise RuntimeError("Go control service did not start")
            time.sleep(0.1)
        info = socket_path.stat()
        assert info.st_uid == 10001 and stat.S_IMODE(info.st_mode) == 0o600
        code, view = control_request(socket_path, "GET")
        assert code == 200 and view["ready"]
        revision = view["revision"]
        updated = dict(config, dnsServers=["223.5.5.5", "1.1.1.1", "223.5.5.5"])
        code, saved = control_request(socket_path, "PUT", {"config": updated, "expectedRevision": revision})
        assert code == 200 and saved["dnsChanged"]
        assert saved["config"]["dnsServers"] == ["223.5.5.5", "1.1.1.1"]
        assert saved["config"] == json.loads(policy.CONFIG.read_text())
        print("pass=Go Unix configuration persisted and DNS order preserved", flush=True)
        code, _ = control_request(socket_path, "PUT", {"config": updated, "expectedRevision": revision})
        assert code == 409
        before = policy.fingerprint(policy.table_state())
        for bad in [
            {"config": dict(updated, blockedCIDRs=["0.0.0.0/0"]), "expectedRevision": saved["revision"]},
            {"config": dict(updated, lanInterfaces=["missing0"]), "expectedRevision": saved["revision"]},
            {"config": dict(updated, lanInterfaces=["eth0; reboot"]), "expectedRevision": saved["revision"]},
            {"config": updated, "expectedRevision": saved["revision"], "command": "reboot"},
        ]:
            code, _ = control_request(socket_path, "PUT", bad)
            assert code == 400
        assert policy.fingerprint(policy.table_state()) == before
        print("pass=Go rejected invalid updates without changing rules", flush=True)
        task = {"config": dict(updated, allow=[]), "expectedRevision": saved["revision"]}
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            statuses = sorted(result[0] for result in pool.map(lambda _: control_request(socket_path, "PUT", task), range(2)))
        assert statuses == [200, 409]
        print("pass=Go concurrent update has one winner and one conflict", flush=True)
        request("app", "10.251.0.3", 18401, False, "removed IP-port exception applied by control API")
        time.sleep(5.3)
        code, view = control_request(socket_path, "GET")
        assert code == 200 and view["ready"]
        persisted = json.loads(policy.CONFIG.read_text())
    finally:
        process.terminate()
        process.wait(timeout=5)
        process.stderr.close()
    request("app", "10.251.0.3", 18400, False, "Go service exit retains previous rules")
    process = subprocess.Popen(command, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    try:
        deadline = time.monotonic() + 6
        while True:
            try:
                code, view = control_request(socket_path, "GET")
                if code == 200 and view["ready"]: break
            except (OSError, ValueError):
                pass
            if process.poll() is not None or time.monotonic() > deadline:
                raise RuntimeError("Go control restart failed")
            time.sleep(0.1)
        assert view["config"] == persisted
        print("pass=Go service restart retains saved configuration", flush=True)
    finally:
        process.terminate()
        process.wait(timeout=5)
        process.stderr.close()

# inner 在隔离环境内加载与生产相同的规则生成器并覆盖关键通信边界。
def inner():
    if os.stat("/proc/self/ns/net").st_ino == os.stat("/proc/1/ns/net").st_ino:
        raise RuntimeError("refuse to test in the host network namespace")
    Path("/run/netns").mkdir(exist_ok=True)
    run(["mount", "-t", "tmpfs", "tmpfs", "/run/netns"])
    run(["ip", "link", "set", "lo", "up"])
    run(["sysctl", "-w", "net.ipv4.ip_forward=1"])
    run(["sysctl", "-w", "net.ipv6.conf.all.forwarding=1"])
    run(["ip", "link", "add", "sne-test", "type", "bridge"])
    run(["ip", "address", "add", "10.250.0.1/24", "dev", "sne-test"])
    run(["ip", "-6", "address", "add", "fd25:1::1/64", "dev", "sne-test", "nodad"])
    run(["ip", "link", "set", "sne-test", "up"])
    peer("app", "app-port", "10.250.0.1/24", "10.250.0.2/24", "fd25:1::1/64", "fd25:1::2/64", "sne-test")
    peer("lan", "lan0", "10.251.0.1/24", "10.251.0.3/24", "fd25:2::1/64", "fd25:2::3/64")
    peer("wan", "wan0", "203.0.113.1/24", "203.0.113.3/24", "2001:db8:3::1/64", "2001:db8:3::3/64")
    processes = []
    try:
        for namespace, port in [("", 18400), ("app", 18400), ("lan", 18400), ("lan", 18401), ("wan", 18400)]:
            command = (["ip", "netns", "exec", namespace] if namespace else []) + [sys.executable, "-u", "-c", SERVER, str(port)]
            processes.append(subprocess.Popen(command, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
        time.sleep(0.5)
        request("", "10.251.0.3", 18400, True, "LAN target reachable before filtering")
        request("app", "10.251.0.3", 18400, True, "application baseline LAN connection")
        policy = GoPolicy()
        with tempfile.TemporaryDirectory(prefix="stacknest-network-test-") as folder:
            policy.CONFIG, policy.STATE = Path(folder) / "config.json", Path(folder) / "state"
            config = {"lanInterfaces": ["lan0"], "blockedCIDRs": [], "allow": [], "dnsServers": ["1.1.1.1"]}
            policy.write_json(policy.CONFIG, config)
            policy.synchronize()
            request("app", "10.251.0.1", 18400, True, "IPv4 host service allowed")
            request("app", "fd25:2::1", 18400, True, "IPv6 host service allowed")
            request("app", "10.251.0.3", 18400, False, "IPv4 other LAN device blocked")
            request("app", "fd25:2::3", 18400, False, "IPv6 other LAN device blocked")
            request("app", "203.0.113.3", 18400, True, "IPv4 public route allowed")
            request("app", "2001:db8:3::3", 18400, True, "IPv6 public route allowed")
            request("lan", "10.250.0.2", 18400, True, "IPv4 application reply to LAN visitor")
            request("lan", "fd25:1::2", 18400, True, "IPv6 application reply to LAN visitor")
            run(["nft", "-f", "-"], input="""add table ip test_nat
add chain ip test_nat ingress { type nat hook prerouting priority -100; policy accept; }
add rule ip test_nat ingress ip daddr 10.251.0.1 tcp dport 18500 dnat to 10.251.0.3:18400
""")
            request("app", "10.251.0.1", 18500, True, "host published service allowed before DNAT")
            request("app", "10.251.0.3", 18400, False, "DNAT allowance does not open LAN addresses")
            config["allow"] = [{"address": "10.251.0.3", "port": 18401, "protocol": "tcp"}]
            policy.write_json(policy.CONFIG, config)
            expected = policy.synchronize()
            request("app", "10.251.0.3", 18401, True, "exact administrator IP and port exception")
            request("app", "10.251.0.3", 18400, False, "exception does not open another port")
            before = policy.fingerprint(policy.table_state())
            policy.write_json(policy.CONFIG, {"lanInterfaces": ["missing0"]})
            try:
                policy.synchronize(expected)
                raise AssertionError("invalid configuration accepted")
            except ValueError:
                pass
            assert policy.fingerprint(policy.table_state()) == before
            request("app", "10.251.0.3", 18400, False, "invalid configuration retains previous rules")
            policy.write_json(policy.CONFIG, config)
            run(["nft", "delete", "table", "inet", policy.TABLE])
            policy.synchronize(expected)
            request("app", "10.251.0.3", 18400, False, "removed table repaired by periodic verification")
            control_checks(policy, config)
    finally:
        for process in processes:
            process.terminate()
        for process in processes:
            process.wait(timeout=5)
    print("network_namespace_checks_passed=true; host_firewall_unchanged=true", flush=True)

# main 外层先创建网络及挂载命名空间，避免测试规则进入真实宿主。
def main():
    if sys.platform != "linux" or os.geteuid() != 0:
        raise SystemExit("requires Linux root; use sudo python3 deploy/check_egress_network.py")
    if "--inner" in sys.argv:
        inner()
    else:
        print(run(["unshare", "--mount", "--net", "--propagation", "private", sys.executable, "-u", str(Path(__file__).resolve()), "--inner"], timeout=90), end="")

if __name__ == "__main__":
    main()
