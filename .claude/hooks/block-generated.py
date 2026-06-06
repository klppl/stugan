#!/usr/bin/env python3
# PreToolUse(Edit|Write): deny edits to generated/build-output paths. client/dist
# is Vite build output served by the daemon (server.static_dir) — any edit there
# is silently clobbered by the next `vite build`. Editing it is always a mistake;
# the source lives in client/src. Returns a deny decision (not a hard error) so
# the model gets a clear redirect instead of a wasted write.
import json
import os
import sys

# Path fragments that must never be hand-edited. Matched against the normalized
# absolute path with forward slashes, so a trailing-slash fragment matches a
# directory prefix anywhere in the tree.
BLOCKED = {
    "/client/dist/": "client/dist is Vite build output — edit the source under client/src and run `npm run build`.",
    "/client/node_modules/": "node_modules is a dependency tree — change package.json and reinstall instead.",
}

try:
    fp = json.load(sys.stdin).get("tool_input", {}).get("file_path", "")
except Exception:
    sys.exit(0)

if not fp:
    sys.exit(0)

norm = os.path.normpath(fp).replace(os.sep, "/")
# normpath strips a trailing slash; re-add boundaries so "/client/dist/" matches
# both the dir itself and files under it.
probe = norm + "/"

for frag, reason in BLOCKED.items():
    if frag in probe:
        print(json.dumps({
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "permissionDecision": "deny",
                "permissionDecisionReason": reason,
            }
        }))
        sys.exit(0)

sys.exit(0)
