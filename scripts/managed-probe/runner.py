"""外层硬超时：每个用例独立进程，25 秒未返回则杀进程组并报告失败。"""

import os
import subprocess
import sys
import time

CASES = [
    "timeout_selftest",
    "state_cd_export_source",
    "prompt_spoofing",
    "stdin_foreground",
    "ctrl_c_sleep",
    "resize_sigwinch",
    "subshell_foreground",
    "bash_self_exit",
    "human_takeover",
    "human_empty_enter",
    "paste_full_write",
]
component = sys.argv[1]
mode = sys.argv[2] if len(sys.argv) > 2 else "local"
extra = sys.argv[3:]
selected = [c for c in CASES if not extra or c in extra]
failed = []
for name in selected:
    argv = [
        sys.executable,
        os.path.join(os.path.dirname(__file__), "probe.py"),
        component,
        mode,
        name,
    ]
    start = time.monotonic()
    try:
        r = subprocess.run(
            argv, check=False, timeout=25, capture_output=True, text=True
        )
        out = (r.stdout + r.stderr).strip()
        print(f"[{name}] rc={r.returncode} {time.monotonic() - start:.1f}s\n{out}")
        if r.returncode != 0:
            failed.append(name)
    except subprocess.TimeoutExpired:
        print(f"[{name}] HARD TIMEOUT 25s")
        failed.append(name)
print("FAILED:", failed if failed else "none")
sys.exit(1 if failed else 0)
