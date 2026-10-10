#!/usr/bin/env python3
"""从已核对的 Moby 默认配置生成应用配额保护；禁止三个修改 XFS 配额属性的 ioctl。"""
import argparse
import hashlib
import json
from pathlib import Path

BASE_SHA256 = "6416b47770785a41ac59073cdc77d9fe98517df2799dc83ef207e622de3053f6"
BLOCKED = {0x401C5820, 0x40086602, 0x40046602}
LOW32 = 0xFFFFFFFF

# allow_prefixes 用互不重叠的二进制前缀表示三个编号的补集，覆盖带高位或符号扩展的调用。
def allow_prefixes(excluded, prefix=0, bits=32):
    if not excluded:
        yield (LOW32 << bits) & LOW32, prefix
        return
    if bits == 0:
        return
    bit = 1 << (bits - 1)
    for set_bit in (False, True):
        subset = {value for value in excluded if bool(value & bit) == set_bit}
        yield from allow_prefixes(subset, prefix | (bit if set_bit else 0), bits - 1)

# generate 移除 ioctl 通配允许规则；不能在 libseccomp 的通配允许后直接叠加窄拒绝规则。
def generate(raw):
    if hashlib.sha256(raw).hexdigest() != BASE_SHA256:
        raise ValueError("Moby baseline changed: review the new official profile before updating its hash")
    profile = json.loads(raw)
    for rule in profile["syscalls"]:
        if "ioctl" in rule["names"]:
            if rule["action"] != "SCMP_ACT_ALLOW" or rule.get("args"):
                raise ValueError("unexpected upstream ioctl rule")
            rule["names"].remove("ioctl")
    profile["syscalls"] = [rule for rule in profile["syscalls"] if rule["names"]]
    for mask, value in allow_prefixes(BLOCKED):
        profile["syscalls"].append({
            "names": ["ioctl"], "action": "SCMP_ACT_ALLOW",
            "args": [{"index": 1, "value": mask, "valueTwo": value, "op": "SCMP_CMP_MASKED_EQ"}],
        })
    for command in sorted(BLOCKED):
        profile["syscalls"].append({
            "names": ["ioctl"], "action": "SCMP_ACT_ERRNO", "errnoRet": 13,
            "args": [{"index": 1, "value": LOW32, "valueTwo": command, "op": "SCMP_CMP_MASKED_EQ"}],
        })
    return profile

# main 只处理调用者给出的官方配置文件，不在构建或运行时联网更新安全策略。
def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("baseline", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    profile = generate(args.baseline.read_bytes())
    args.output.write_text(json.dumps(profile, separators=(",", ":")) + "\n", encoding="utf-8")

if __name__ == "__main__":
    main()
