// Tiny shared UI state for layout chrome that two sibling components both
// need to read and write (sidebar drawer, members drawer, current breakpoint).
// Keeping this out of `connection.ts` so view-only concerns don't bleed into
// the protocol/store layer.

import { onMounted, onUnmounted, reactive } from "vue";
import { connection, foldTarget } from "./connection";
import { proxied } from "./links";

export interface ActiveImage {
  url: string;
  proxiedUrl: string;
  alt?: string;
}

const MOBILE_QUERY = "(max-width: 720px)";
const mq = typeof window !== "undefined" ? window.matchMedia(MOBILE_QUERY) : null;
const startMobile = mq?.matches ?? false;

// membersOpen has different defaults per breakpoint: on desktop the member
// panel is shown by default (the toggle collapses it via display:none in
// the desktop CSS); on mobile it's a slide-in drawer that defaults closed.
// One state, two display strategies — see the .members rules in style.css.
export const ui = reactive({
  sidebarOpen: false,
  membersOpen: !startMobile,
  isMobile: startMobile,
  activeImage: null as ActiveImage | null,
  // Channel browser (network id) and channel-details modal. They live here,
  // not in TopBar, so the Cmd-K palette can open them when the top bar is
  // hidden by the "Show top bar" setting.
  browseNet: null as string | null,
  inspectorOpen: false,
  shortcutsOpen: false,
});

// macOS uses Option (Alt) to type characters — on Swedish/German/French
// layouts Option+2 is "@" and Option+8/9 are brackets — so the Alt+digit and
// Alt+A buffer shortcuts would eat real text there. Mac gets Ctrl+digit
// instead; Alt+Shift+↑/↓ works everywhere.
export const isMac =
  typeof navigator !== "undefined" && /Mac|iPhone|iPad|iPod/.test(navigator.platform || navigator.userAgent);
export const NUM_MOD = isMac ? "Ctrl" : "Alt";

export interface BufRef {
  network: string;
  buffer: string;
}

// bufferOrder is the sidebar order flattened: each network's status buffer
// (its header row), then its channels and queries. Alt+↑/↓, Alt+1–9 and
// next-unread all walk this list.
export function bufferOrder(): BufRef[] {
  return connection.store.networks.flatMap((n) =>
    [...n.buffers.filter((b) => b.kind === "status"), ...n.buffers.filter((b) => b.kind !== "status")].map((b) => ({
      network: n.id,
      buffer: b.name,
    })),
  );
}

function activeIndex(order: BufRef[]): number {
  const a = connection.store.active;
  if (!a) return -1;
  const f = foldTarget(a.buffer);
  return order.findIndex((o) => o.network === a.network && foldTarget(o.buffer) === f);
}

// stepBuffer moves to the previous/next buffer in sidebar order, wrapping.
export function stepBuffer(dir: 1 | -1) {
  const order = bufferOrder();
  if (!order.length) return;
  const cur = activeIndex(order);
  const next = order[(cur + dir + order.length) % order.length];
  connection.select(next.network, next.buffer);
}

// selectNth opens the nth buffer (0-based) in sidebar order, if there is one.
export function selectNth(i: number) {
  const b = bufferOrder()[i];
  if (b) connection.select(b.network, b.buffer);
}

// nextUnread jumps to the next buffer (in sidebar order from the current one,
// wrapping; dir -1 walks backwards) that needs attention — buffers that
// mention you first, then any with unread activity. Returns false when
// everything is read.
export function nextUnread(dir: 1 | -1 = 1): boolean {
  const order = bufferOrder();
  const n = order.length;
  if (!n) return false;
  const cur = activeIndex(order);
  const walk: BufRef[] = [];
  for (let k = 1; k <= n; k++) walk.push(order[(((cur + dir * k) % n) + n) % n]);
  const bufOf = (r: BufRef) =>
    connection.store.networks.find((x) => x.id === r.network)?.buffers.find((b) => b.name === r.buffer);
  const pick = walk.find((r) => (bufOf(r)?.highlight ?? 0) > 0) ?? walk.find((r) => (bufOf(r)?.unread ?? 0) > 0);
  if (!pick) return false;
  connection.select(pick.network, pick.buffer);
  return true;
}

// Reset drawer/panel state on a viewport-class change so neither side
// inherits a stale "open" value across breakpoints. Going to mobile closes
// everything (drawers must start hidden); going to desktop closes the
// sidebar (it docks back into the static layout) and re-opens the member
// panel to its default-visible desktop state.
mq?.addEventListener("change", (e) => {
  ui.isMobile = e.matches;
  ui.sidebarOpen = false;
  ui.membersOpen = !e.matches;
});

// On desktop the member list isn't a drawer but a docked panel the user
// toggled on purpose, so leave it alone there — otherwise opening Settings
// (or a friend's DM) silently hid it for good.
export function closeDrawers() {
  ui.sidebarOpen = false;
  if (ui.isMobile) ui.membersOpen = false;
}

export function toggleSidebar() {
  ui.sidebarOpen = !ui.sidebarOpen;
  if (ui.sidebarOpen) ui.membersOpen = false;
}

export function toggleMembers() {
  ui.membersOpen = !ui.membersOpen;
  if (ui.membersOpen) ui.sidebarOpen = false;
}

export function openImageViewer(url: string, alt?: string) {
  ui.activeImage = {
    url,
    proxiedUrl: proxied(url),
    alt,
  };
}

export function closeImageViewer() {
  ui.activeImage = null;
}

// Drawer swipe navigation (mobile only). A horizontal drag across the
// viewport opens/closes the side drawers the same way the toggle buttons
// do: swipe right reveals the channel sidebar (or first dismisses the
// members drawer if it's open); swipe left reveals the members drawer (or
// dismisses the sidebar). Only one drawer is ever open, matching the
// toggle* helpers above.
function swipeRight() {
  if (connection.store.view !== "chat") return;
  if (ui.membersOpen) ui.membersOpen = false;
  else ui.sidebarOpen = true;
}
function swipeLeft() {
  if (connection.store.view !== "chat") return;
  if (ui.sidebarOpen) ui.sidebarOpen = false;
  else ui.membersOpen = true;
}

// useSwipeNav wires the gesture to window touch events for the lifetime of
// the calling component (mount it once, at the app root). A swipe counts
// only when it travels SWIPE_MIN px horizontally and that horizontal
// travel dominates the vertical by SWIPE_RATIO — so vertical message
// scrolling and diagonal flicks never trigger a drawer. Listeners are
// passive (we only read the touch, never preventDefault), so scrolling
// stays smooth.
export function useSwipeNav() {
  const SWIPE_MIN = 60;
  const SWIPE_RATIO = 1.8;
  let startX = 0;
  let startY = 0;
  let tracking = false;

  function onStart(ev: TouchEvent) {
    // Single-finger only — ignore pinch/zoom, multi-touch, or when image viewer is open.
    if (!ui.isMobile || ev.touches.length !== 1 || ui.activeImage) {
      tracking = false;
      return;
    }
    const t = ev.touches[0];
    startX = t.clientX;
    startY = t.clientY;
    tracking = true;
  }
  function onEnd(ev: TouchEvent) {
    if (!tracking) return;
    tracking = false;
    const t = ev.changedTouches[0];
    if (!t) return;
    const dx = t.clientX - startX;
    const dy = t.clientY - startY;
    if (Math.abs(dx) < SWIPE_MIN || Math.abs(dx) < Math.abs(dy) * SWIPE_RATIO) return;
    if (dx > 0) swipeRight();
    else swipeLeft();
  }

  onMounted(() => {
    window.addEventListener("touchstart", onStart, { passive: true });
    window.addEventListener("touchend", onEnd, { passive: true });
  });
  onUnmounted(() => {
    window.removeEventListener("touchstart", onStart);
    window.removeEventListener("touchend", onEnd);
  });
}
