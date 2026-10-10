"""验证 ioctl 补集策略，包括 64 位参数高位、符号扩展和 libseccomp 通配覆盖回归。"""
import json
from pathlib import Path
import random
import unittest
from generate_quota_seccomp import BLOCKED, allow_prefixes, generate

class QuotaSeccompTests(unittest.TestCase):
    # test_prefix_partition 保护低 32 位命令，不能通过设置高位或符号扩展绕过。
    def test_prefix_partition(self):
        prefixes = list(allow_prefixes(BLOCKED))
        samples = [0, 0xFFFFFFFF, 0x801C581F, 0x5421, 0x541B]
        rng = random.Random(9173)
        samples += [rng.getrandbits(64) for _ in range(1000)]
        for blocked in BLOCKED:
            samples += [blocked - 1, blocked, blocked + 1, blocked | (1 << 32), blocked | 0xFFFFFFFF00000000]
        for command in samples:
            matches = sum(command & mask == value for mask, value in prefixes)
            self.assertEqual(matches, 0 if command & 0xFFFFFFFF in BLOCKED else 1, hex(command))

    # test_bundled_policy 无 ioctl 通配允许规则，实际嵌入配置与前缀补集一致。
    def test_bundled_policy(self):
        path = Path(__file__).resolve().parents[1] / "internal/dockerrunner/seccomp-quota.json"
        profile = json.loads(path.read_text())
        actual = []
        for rule in profile["syscalls"]:
            if "ioctl" not in rule["names"] or rule["action"] != "SCMP_ACT_ALLOW":
                continue
            self.assertEqual(rule["names"], ["ioctl"])
            self.assertEqual(len(rule["args"]), 1)
            arg = rule["args"][0]
            self.assertEqual(arg["index"], 1)
            self.assertEqual(arg["op"], "SCMP_CMP_MASKED_EQ")
            actual.append((arg["value"], arg["valueTwo"]))
        self.assertCountEqual(actual, list(allow_prefixes(BLOCKED)))

    # test_changed_baseline_requires_review 安全策略不能随上游修改而静默放宽。
    def test_changed_baseline_requires_review(self):
        with self.assertRaises(ValueError):
            generate(b'{"defaultAction":"SCMP_ACT_ALLOW","syscalls":[]}')

if __name__ == "__main__":
    unittest.main()
