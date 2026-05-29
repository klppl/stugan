#!/usr/bin/env python3
# PostToolUse(Edit|Write): the wire protocol lives in two hand-synced files —
# internal/proto/proto.go (source of truth) and client/src/proto/events.ts.
# When one is touched, inject a reminder to mirror the other and wire the
# handlers. See docs/protocol.md and CLAUDE.md ("kept in sync by hand").
import json
import sys

try:
    fp = json.load(sys.stdin).get("tool_input", {}).get("file_path", "")
except Exception:
    sys.exit(0)

if fp.endswith("internal/proto/proto.go"):
    msg = (
        "You edited internal/proto/proto.go (the wire-protocol source of truth). "
        "Mirror any struct/field/const change into client/src/proto/events.ts, and "
        "wire handlers: c2s in server.route (internal/server/server.go), s2c in "
        "connection.ts onFrame. Field json tags must match TS keys exactly."
    )
elif fp.endswith("client/src/proto/events.ts"):
    msg = (
        "You edited client/src/proto/events.ts. Confirm internal/proto/proto.go "
        "(the source of truth) matches — every TS interface field needs a Go struct "
        "field with the same json tag, and every T.* discriminator needs a proto.T* const."
    )
else:
    sys.exit(0)

print(json.dumps({
    "hookSpecificOutput": {
        "hookEventName": "PostToolUse",
        "additionalContext": msg,
    }
}))
