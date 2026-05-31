// Tiny shared UI state for layout chrome that two sibling components both
// need to read and write (sidebar drawer, members drawer, current breakpoint).
// Keeping this out of `connection.ts` so view-only concerns don't bleed into
// the protocol/store layer.

import { onMounted, onUnmounted, reactive } from "vue";

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
});

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

export function closeDrawers() {
  ui.sidebarOpen = false;
  ui.membersOpen = false;
}

export function toggleSidebar() {
  ui.sidebarOpen = !ui.sidebarOpen;
  if (ui.sidebarOpen) ui.membersOpen = false;
}

export function toggleMembers() {
  ui.membersOpen = !ui.membersOpen;
  if (ui.membersOpen) ui.sidebarOpen = false;
}

// Drawer swipe navigation (mobile only). A horizontal drag across the
// viewport opens/closes the side drawers the same way the toggle buttons
// do: swipe right reveals the channel sidebar (or first dismisses the
// members drawer if it's open); swipe left reveals the members drawer (or
// dismisses the sidebar). Only one drawer is ever open, matching the
// toggle* helpers above.
function swipeRight() {
  if (ui.membersOpen) ui.membersOpen = false;
  else ui.sidebarOpen = true;
}
function swipeLeft() {
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
    // Single-finger only — ignore pinch/zoom and multi-touch.
    if (!ui.isMobile || ev.touches.length !== 1) {
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
