// Throwaway end-to-end harness for the stugan daemon.
//
// Connects to a locally-running daemon over the typed-JSON WebSocket, sends
// `proto` frames, and asserts on inbound frames. There is no mock IRC server,
// so this is driven against a real daemon (typically connected to Libera).
// Node 18+ has a global WebSocket. Run with: node e2e.mjs
//
// Frame envelope is {t, id, d}; see internal/proto/proto.go for payloads.
// Edit STEPS for the behavior under test, then watch the pass/fail output.

const URL = process.env.STUGAN_WS || "ws://127.0.0.1:8080/ws";
const TIMEOUT_MS = 15000;

// A random suffix keeps nicks/channels unique across runs (good Libera
// citizenship: pick a quiet channel and disconnect when done).
const rand = Math.floor(Math.random() * 1e5);

// Each step either sends a frame, or waits for an inbound frame matching a
// predicate and asserts on it. Customize for your case.
const STEPS = [
  // Example: expect the server's hello on connect.
  { wait: (f) => f.t === "hello", assert: (f) => f.d?.protocol >= 1, name: "hello received" },

  // Example: add + connect a network (uncomment and adjust).
  // { send: { t: "net:add", id: "add1", d: {
  //     name: "libera", addr: "irc.libera.chat:6697", tls: true,
  //     nick: `stugan${rand}`, channels: [`#stugan-test${rand}`],
  //   } } },
  // { wait: (f) => f.t === "net:update" && f.d?.state === "connected", name: "network connected", timeout: 30000 },

  // Example: send a message and expect the echo back.
  // { send: { t: "msg:send", id: "m1", d: { network: "libera", buffer: `#stugan-test${rand}`, text: "hello from e2e" } } },
  // { wait: (f) => f.t === "msg" && f.d?.text === "hello from e2e", assert: (f) => f.d?.self === true, name: "own message echoed" },
];

const ws = new WebSocket(URL);
const inbound = [];
const waiters = [];
let failed = false;

function log(ok, name, extra = "") {
  const tag = ok ? "PASS" : "FAIL";
  if (!ok) failed = true;
  console.log(`[${tag}] ${name}${extra ? "  " + extra : ""}`);
}

ws.addEventListener("message", (ev) => {
  let frame;
  try { frame = JSON.parse(ev.data); } catch { return; }
  inbound.push(frame);
  for (let i = waiters.length - 1; i >= 0; i--) {
    if (waiters[i].pred(frame)) {
      waiters[i].resolve(frame);
      waiters.splice(i, 1);
    }
  }
});

function waitFor(pred, timeout) {
  // Match against frames already buffered, else park a waiter.
  const hit = inbound.find(pred);
  if (hit) return Promise.resolve(hit);
  return new Promise((resolve, reject) => {
    const w = { pred, resolve };
    waiters.push(w);
    setTimeout(() => {
      const idx = waiters.indexOf(w);
      if (idx >= 0) { waiters.splice(idx, 1); reject(new Error("timeout")); }
    }, timeout ?? TIMEOUT_MS);
  });
}

async function run() {
  for (const step of STEPS) {
    if (step.send) {
      ws.send(JSON.stringify(step.send));
      continue;
    }
    if (step.wait) {
      try {
        const frame = await waitFor(step.wait, step.timeout);
        const ok = step.assert ? !!step.assert(frame) : true;
        log(ok, step.name || "wait", ok ? "" : JSON.stringify(frame));
      } catch {
        log(false, step.name || "wait", "(timeout)");
      }
    }
  }
}

ws.addEventListener("open", async () => {
  console.log(`connected to ${URL}`);
  await run();
  ws.close();
  console.log(failed ? "\nRESULT: FAIL" : "\nRESULT: PASS");
  process.exit(failed ? 1 : 0);
});

ws.addEventListener("error", (e) => {
  console.error("ws error:", e.message || e);
  process.exit(1);
});
