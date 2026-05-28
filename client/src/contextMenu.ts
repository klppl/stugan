import { onMounted, onUnmounted, ref, type Ref } from "vue";

// useContextMenu provides the shared boilerplate for a right-click /
// long-press floating menu: a reactive payload (`state`), positioning at
// the cursor or touch point with viewport clamping, dismissal on
// click-outside or Escape, and a touch long-press timer that opens the
// same menu after 500ms unless the finger moves.
//
// The menu items themselves are caller-specific — this composable only
// manages position and lifecycle. Render with:
//
//   <div v-if="ctx.state.value" class="ctx-menu"
//        :style="{ left: ctx.state.value.x + 'px', top: ctx.state.value.y + 'px' }">
//     ...items, each calling ctx.close() in their click handler
//   </div>
//
// Width/height are the approximate menu dimensions used to clamp the
// position so it doesn't overflow the viewport. Defaults match the
// existing .ctx-menu CSS.

export interface CtxState<T> {
  payload: T;
  x: number;
  y: number;
}

export function useContextMenu<T>(opts: { width?: number; height?: number } = {}) {
  const W = opts.width ?? 220;
  const H = opts.height ?? 200;
  const state: Ref<CtxState<T> | null> = ref(null);

  // Long-press support. lpTimer fires after LP_DELAY ms; lpStart records
  // the touch origin so a small drift cancels the press (preserving the
  // list's scroll gesture).
  const LP_DELAY = 500;
  const LP_SLOP = 8;
  let lpTimer: ReturnType<typeof setTimeout> | null = null;
  let lpStart: { x: number; y: number } | null = null;
  // suppressNextClick prevents the long-press-triggered open from also
  // counting as a tap on the underlying element when the finger lifts.
  let suppressNextClick = false;

  function open(payload: T, x: number, y: number) {
    state.value = {
      payload,
      x: Math.min(x, window.innerWidth - W - 8),
      y: Math.min(y, window.innerHeight - H - 8),
    };
  }
  function close() {
    state.value = null;
  }
  function onContext(payload: T, ev: MouseEvent) {
    ev.preventDefault();
    open(payload, ev.clientX, ev.clientY);
  }
  function onTouchStart(payload: T, ev: TouchEvent) {
    const t = ev.touches[0];
    if (!t) return;
    lpStart = { x: t.clientX, y: t.clientY };
    cancelLp();
    lpTimer = setTimeout(() => {
      lpTimer = null;
      suppressNextClick = true;
      open(payload, lpStart!.x, lpStart!.y);
    }, LP_DELAY);
  }
  function onTouchMove(ev: TouchEvent) {
    if (!lpStart || !lpTimer) return;
    const t = ev.touches[0];
    if (!t) return;
    const dx = t.clientX - lpStart.x;
    const dy = t.clientY - lpStart.y;
    if (dx * dx + dy * dy > LP_SLOP * LP_SLOP) cancelLp();
  }
  function cancelLp() {
    if (lpTimer) {
      clearTimeout(lpTimer);
      lpTimer = null;
    }
  }
  // shouldSuppressClick returns true exactly once after a long-press
  // opened the menu; callers wrap the underlying tap action with it.
  function shouldSuppressClick(): boolean {
    if (!suppressNextClick) return false;
    suppressNextClick = false;
    return true;
  }

  function onDocMouseDown(ev: MouseEvent) {
    if (!state.value) return;
    if (!(ev.target as HTMLElement)?.closest?.(".ctx-menu")) close();
  }
  function onDocTouchStart(ev: TouchEvent) {
    if (!state.value) return;
    if (!(ev.target as HTMLElement)?.closest?.(".ctx-menu")) close();
  }
  function onKey(ev: KeyboardEvent) {
    if (ev.key === "Escape") close();
  }

  onMounted(() => {
    document.addEventListener("mousedown", onDocMouseDown);
    document.addEventListener("touchstart", onDocTouchStart, { passive: true });
    document.addEventListener("keydown", onKey);
  });
  onUnmounted(() => {
    document.removeEventListener("mousedown", onDocMouseDown);
    document.removeEventListener("touchstart", onDocTouchStart);
    document.removeEventListener("keydown", onKey);
    cancelLp();
  });

  return {
    state,
    open,
    close,
    onContext,
    onTouchStart,
    onTouchMove,
    cancelLp,
    shouldSuppressClick,
  };
}
