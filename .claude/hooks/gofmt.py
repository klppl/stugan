#!/usr/bin/env python3
# PostToolUse(Edit|Write): gofmt -w any edited .go file so CI's gofmt gate
# (ci.yml fails on `gofmt -l .` output) never trips. Silent on success.
import json
import subprocess
import sys

try:
    fp = json.load(sys.stdin).get("tool_input", {}).get("file_path", "")
except Exception:
    sys.exit(0)

if fp.endswith(".go"):
    subprocess.run(["gofmt", "-w", fp], check=False)
