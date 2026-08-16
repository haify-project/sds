#!/usr/bin/env python3
"""Dump every sds-cli subcommand's --help into one markdown file for the Copilot's
knowledge base.

The Copilot knows SDS's architecture from the code graph and its operational
history from the incident notes, but neither of those carries a flag spelling.
Asked for a runnable command it used to assemble a plausible one — `gateway
create nfs --vip ...` instead of `gateway nfs create --service-ip ...` — which
reads as authoritative and fails on paste. The binary's own help is the only
source that cannot drift from the binary, so it is what gets ingested.

Re-run after any CLI change and re-ingest:

    python3 dump-cli-help.py > /tmp/clidoc/sds-cli-help.md
    oss-agent refresh /tmp/clidoc     # purges the old copy first

Usage: dump-cli-help.py [path-to-sds-cli]
"""

import subprocess
import sys

CLI = sys.argv[1] if len(sys.argv) > 1 else "/opt/sds/bin/sds-cli"

HEADER = """# sds-cli 命令参考(由 sds-cli --help 递归导出)

本文件是 SDS 命令行工具的权威语法参考:子命令的层级顺序、每个 flag 的准确拼写、
哪些是必填、默认值是什么,全部直接来自二进制自身的 --help 输出,不是人工整理的。

回答任何涉及 sds-cli 命令行的问题时,命令必须照此处的语法给出。注意子命令的顺序
是 `sds-cli <名词> <协议> <动作>`(例如 `gateway nfs create`),不是 `<名词> <动作>
<协议>`;网关的服务 IP 参数叫 --service-ip,不叫 --vip。
"""


def run(path):
    try:
        r = subprocess.run(
            [CLI] + path + ["--help"], capture_output=True, text=True, timeout=30
        )
    except (OSError, subprocess.TimeoutExpired) as err:
        print(f"warning: {' '.join(path)}: {err}", file=sys.stderr)
        return ""
    return (r.stdout or "") + (r.stderr or "")


def subcommands(text):
    """Names listed under 'Available Commands:', minus cobra's own two."""
    subs, grab = [], False
    for line in text.splitlines():
        if line.startswith("Available Commands:"):
            grab = True
            continue
        if grab and (
            line.startswith("Flags:")
            or line.startswith("Additional")
            or line.startswith("Use ")
        ):
            grab = False
            continue
        if grab and line.strip():
            name = line.split()[0]
            if name not in ("help", "completion"):
                subs.append(name)
    return subs


def walk(path, seen, out):
    # A command reachable by two routes would otherwise be emitted twice, and a
    # duplicated chunk is a duplicated retrieval candidate.
    key = " ".join(path)
    if key in seen:
        return
    seen.add(key)
    text = run(path)
    if not text.strip():
        return
    title = "sds-cli " + key if key else "sds-cli"
    out.append("### " + title + "\n\n```\n" + text.rstrip() + "\n```\n")
    for sub in subcommands(text):
        walk(path + [sub], seen, out)


def main():
    out = []
    walk([], set(), out)
    if not out:
        print(f"error: {CLI} produced no help output", file=sys.stderr)
        return 1
    sys.stdout.write(HEADER + "\n" + "\n".join(out))
    print(f"sections: {len(out)}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
