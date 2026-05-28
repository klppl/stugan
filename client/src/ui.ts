// Tiny shared UI state for layout chrome that two sibling components both
// need to read and write (sidebar drawer, members drawer, current breakpoint).
// Keeping this out of `connection.ts` so view-only concerns don't bleed into
// the protocol/store layer.

import { reactive } from "vue";

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
