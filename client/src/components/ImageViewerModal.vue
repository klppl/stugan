<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import type { ActiveImage } from "../ui";
import { connection } from "../connection";
import Icon from "./Icon.vue";

const props = defineProps<{
  image: ActiveImage;
}>();

const emit = defineEmits<{
  close: [];
}>();

const MIN_SCALE = 1;
const ZOOM_STEP = 1.5;
const DRAG_THRESHOLD = 6;

// Zoom state. `scale` is relative to the fitted (on-screen) size; `tx`/`ty`
// pan the image in screen pixels. Applied as a CSS transform on the <img>.
const scale = ref(1);
const tx = ref(0);
const ty = ref(0);
const animate = ref(false);
const naturalWidth = ref(0);
const fitWidth = ref(0);
const imgEl = ref<HTMLImageElement | null>(null);

const isZoomed = computed(() => scale.value > MIN_SCALE + 0.001);

// Fitted size → original pixel size ratio (1 for images smaller than the screen).
const naturalRatio = computed(() =>
  fitWidth.value && naturalWidth.value ? naturalWidth.value / fitWidth.value : 1,
);

// Allow zooming well past 1:1 regardless of how large the source image is.
const maxScale = computed(() => Math.max(8, naturalRatio.value * 4));

// Zoom level as a percentage of the image's original pixel size.
const zoomPercent = computed(() => Math.round((scale.value * 100) / naturalRatio.value));

const isDragging = ref(false);
const dragOffsetY = ref(0);
let closed = false;

// Active pointers for pan / pinch / swipe-to-dismiss.
const pointers = new Map<number, { x: number; y: number }>();
let gesture: "none" | "pan" | "pinch" | "swipe" = "none";
let startX = 0;
let startY = 0;
let lastX = 0;
let lastY = 0;
let pinchDist = 0;
let moved = false;

function dismiss(fromHistory = false) {
  if (closed) return;
  closed = true;
  emit("close");
  if (!fromHistory && typeof window !== "undefined" && history.state?.modal === "image-viewer") {
    history.back();
  }
}

function onPopState() {
  dismiss(true);
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === "Escape") {
    e.preventDefault();
    dismiss();
  } else if (e.key === "+" || e.key === "=") {
    e.preventDefault();
    zoomIn();
  } else if (e.key === "-" || e.key === "_") {
    e.preventDefault();
    zoomOut();
  } else if (e.key === "0") {
    e.preventDefault();
    resetZoom();
  }
}

function clampPan() {
  const img = imgEl.value;
  if (!img) return;
  const limitX = Math.max(0, (img.offsetWidth * scale.value - window.innerWidth) / 2);
  const limitY = Math.max(0, (img.offsetHeight * scale.value - window.innerHeight) / 2);
  tx.value = Math.min(limitX, Math.max(-limitX, tx.value));
  ty.value = Math.min(limitY, Math.max(-limitY, ty.value));
}

// Zoom to `next`, keeping the screen point (px, py) fixed under the cursor/fingers.
// Without a point, zooms around the current view center.
function zoomTo(next: number, px?: number, py?: number, smooth = false) {
  const img = imgEl.value;
  const target = Math.min(maxScale.value, Math.max(MIN_SCALE, next));
  const ratio = target / scale.value;
  animate.value = smooth;
  const wrapper = img?.parentElement;
  if (wrapper && px !== undefined && py !== undefined) {
    // The wrapper is laid out at the image's untransformed size, so its center
    // is the scale origin. (The <img> rect can lag behind until Vue re-renders.)
    const rect = wrapper.getBoundingClientRect();
    const cx = rect.left + rect.width / 2;
    const cy = rect.top + rect.height / 2;
    tx.value = px - cx - ratio * (px - cx - tx.value);
    ty.value = py - cy - ratio * (py - cy - ty.value);
  } else {
    tx.value *= ratio;
    ty.value *= ratio;
  }
  scale.value = target;
  if (!isZoomed.value) {
    tx.value = 0;
    ty.value = 0;
  }
  clampPan();
}

function zoomIn() {
  zoomTo(scale.value * ZOOM_STEP, undefined, undefined, true);
}

function zoomOut() {
  zoomTo(scale.value / ZOOM_STEP, undefined, undefined, true);
}

function resetZoom() {
  zoomTo(MIN_SCALE, undefined, undefined, true);
}

function onWheel(e: WheelEvent) {
  // Trackpad pinch arrives as ctrl+wheel with small deltas; mouse wheels use larger ones.
  const unit = e.deltaMode === 1 ? 16 : 1;
  const intensity = e.ctrlKey ? 0.01 : 0.0025;
  const delta = Math.max(-100, Math.min(100, e.deltaY * unit));
  zoomTo(scale.value * Math.exp(-delta * intensity), e.clientX, e.clientY);
}

function onImageClick(e: MouseEvent) {
  if (moved) return;
  if (isZoomed.value) {
    resetZoom();
  } else {
    // Jump to 1:1 for large images, otherwise a comfortable 2.5x.
    const target = naturalRatio.value > 1.5 ? naturalRatio.value : 2.5;
    zoomTo(target, e.clientX, e.clientY, true);
  }
}

function measure() {
  const img = imgEl.value;
  if (!img) return;
  naturalWidth.value = img.naturalWidth;
  fitWidth.value = img.offsetWidth;
}

function onResize() {
  measure();
  clampPan();
}

async function copyLink() {
  try {
    await navigator.clipboard.writeText(props.image.url);
    connection.showToast("Image link copied to clipboard", undefined, "info");
  } catch {
    connection.showToast("Failed to copy link", undefined, "error");
  }
}

// Pointer gestures: drag to pan when zoomed, two-finger pinch to zoom,
// and single-finger swipe-to-dismiss when not zoomed.
function onPointerDown(e: PointerEvent) {
  if (e.pointerType === "mouse" && e.button !== 0) return;
  try {
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  } catch {
    // Capture is best-effort; gestures still work without it.
  }
  pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });
  animate.value = false;

  if (pointers.size === 2) {
    const [p1, p2] = [...pointers.values()];
    pinchDist = Math.hypot(p1.x - p2.x, p1.y - p2.y);
    gesture = "pinch";
    isDragging.value = false;
    dragOffsetY.value = 0;
    moved = true;
    return;
  }
  if (pointers.size > 2) return;

  startX = lastX = e.clientX;
  startY = lastY = e.clientY;
  moved = false;
  if (isZoomed.value) {
    gesture = "pan";
  } else if (e.pointerType === "touch") {
    gesture = "swipe";
    isDragging.value = true;
    dragOffsetY.value = 0;
  } else {
    gesture = "none";
  }
}

function onPointerMove(e: PointerEvent) {
  if (!pointers.has(e.pointerId)) return;
  pointers.set(e.pointerId, { x: e.clientX, y: e.clientY });

  if (gesture === "pinch" && pointers.size >= 2) {
    const [p1, p2] = [...pointers.values()];
    const dist = Math.hypot(p1.x - p2.x, p1.y - p2.y);
    if (pinchDist > 0) {
      zoomTo(scale.value * (dist / pinchDist), (p1.x + p2.x) / 2, (p1.y + p2.y) / 2);
    }
    pinchDist = dist;
    return;
  }

  if (Math.hypot(e.clientX - startX, e.clientY - startY) > DRAG_THRESHOLD) moved = true;

  if (gesture === "pan") {
    tx.value += e.clientX - lastX;
    ty.value += e.clientY - lastY;
    clampPan();
  } else if (gesture === "swipe") {
    dragOffsetY.value = e.clientY - startY;
  }
  lastX = e.clientX;
  lastY = e.clientY;
}

function onPointerUp(e: PointerEvent) {
  if (!pointers.delete(e.pointerId)) return;

  if (gesture === "swipe" && pointers.size === 0) {
    isDragging.value = false;
    // If dragged far enough (> 70px), dismiss modal; otherwise spring back.
    if (Math.abs(dragOffsetY.value) > 70) {
      dismiss();
    } else {
      dragOffsetY.value = 0;
    }
  }

  if (pointers.size === 1) {
    // Pinch ended with one finger still down: continue panning from there.
    const [p] = [...pointers.values()];
    lastX = p.x;
    lastY = p.y;
    gesture = isZoomed.value ? "pan" : "none";
  } else if (pointers.size === 0) {
    gesture = "none";
  }
}

const hostName = computed(() => {
  try {
    return new URL(props.image.url).hostname.replace(/^www\./, "");
  } catch {
    return "";
  }
});

const fileName = computed(() => {
  try {
    const path = new URL(props.image.url).pathname;
    const name = path.split("/").pop();
    return name ? decodeURIComponent(name) : "";
  } catch {
    return "";
  }
});

const backdropOpacity = computed(() => {
  if (!isDragging.value || dragOffsetY.value === 0) return 0.9;
  const ratio = Math.min(Math.abs(dragOffsetY.value) / 250, 0.7);
  return Math.max(0.2, 0.9 - ratio);
});

onMounted(() => {
  if (typeof window !== "undefined") {
    history.pushState({ modal: "image-viewer" }, "");
    window.addEventListener("popstate", onPopState);
    window.addEventListener("keydown", onKeydown);
    window.addEventListener("resize", onResize);
  }
});

onUnmounted(() => {
  if (typeof window !== "undefined") {
    window.removeEventListener("popstate", onPopState);
    window.removeEventListener("keydown", onKeydown);
    window.removeEventListener("resize", onResize);
  }
});
</script>

<template>
  <div
    class="image-viewer-backdrop"
    role="dialog"
    aria-modal="true"
    aria-label="Image viewer"
    :style="{ backgroundColor: `rgba(10, 12, 16, ${backdropOpacity})` }"
    @click.self="dismiss()"
  >
    <!-- Top toolbar / header -->
    <header class="image-viewer-header" @click.stop>
      <div class="image-viewer-info">
        <span v-if="fileName" class="image-viewer-filename" :title="fileName">{{ fileName }}</span>
        <span v-if="hostName" class="image-viewer-host">{{ hostName }}</span>
      </div>

      <div class="image-viewer-actions">
        <div class="image-viewer-zoom" role="group" aria-label="Zoom">
          <button
            type="button"
            class="image-viewer-btn"
            title="Zoom out (−)"
            aria-label="Zoom out"
            :disabled="!isZoomed"
            @click="zoomOut"
          >
            <svg aria-hidden="true" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
              <circle cx="11" cy="11" r="8" />
              <line x1="21" y1="21" x2="16.65" y2="16.65" />
              <line x1="8" y1="11" x2="14" y2="11" />
            </svg>
          </button>
          <button
            type="button"
            class="image-viewer-btn image-viewer-zoom-level"
            title="Fit to screen (0)"
            aria-label="Fit to screen"
            :disabled="!isZoomed"
            @click="resetZoom"
          >
            {{ zoomPercent }}%
          </button>
          <button
            type="button"
            class="image-viewer-btn"
            title="Zoom in (+)"
            aria-label="Zoom in"
            :disabled="scale >= maxScale"
            @click="zoomIn"
          >
            <svg aria-hidden="true" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
              <circle cx="11" cy="11" r="8" />
              <line x1="21" y1="21" x2="16.65" y2="16.65" />
              <line x1="11" y1="8" x2="11" y2="14" />
              <line x1="8" y1="11" x2="14" y2="11" />
            </svg>
          </button>
        </div>

        <button
          type="button"
          class="image-viewer-btn"
          title="Copy image link"
          aria-label="Copy image link"
          @click="copyLink"
        >
          <svg aria-hidden="true" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
            <rect x="9" y="9" width="13" height="13" rx="2" ry="2" />
            <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
          </svg>
        </button>

        <a
          :href="image.url"
          target="_blank"
          rel="noopener noreferrer"
          class="image-viewer-btn"
          title="Open original in browser"
          aria-label="Open original in browser"
        >
          <svg aria-hidden="true" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6" />
            <polyline points="15 3 21 3 21 9" />
            <line x1="10" y1="14" x2="21" y2="3" />
          </svg>
        </a>

        <button
          type="button"
          class="image-viewer-btn close"
          title="Close (Esc)"
          aria-label="Close"
          @click="dismiss()"
        >
          <Icon name="x" :size="18" />
        </button>
      </div>
    </header>

    <!-- Content area -->
    <div
      class="image-viewer-body"
      @click.self="dismiss()"
      @wheel.prevent="onWheel"
    >
      <div
        class="image-viewer-content"
        :style="{
          transform: `translateY(${dragOffsetY}px)`,
          transition: isDragging ? 'none' : 'transform 0.2s cubic-bezier(0.2, 0, 0, 1)'
        }"
        @pointerdown="onPointerDown"
        @pointermove="onPointerMove"
        @pointerup="onPointerUp"
        @pointercancel="onPointerUp"
        @click="onImageClick"
      >
        <img
          ref="imgEl"
          :src="image.proxiedUrl"
          :alt="image.alt || fileName || 'Image'"
          class="image-viewer-img"
          :class="{ 'zoomed': isZoomed }"
          :style="{
            transform: `translate(${tx}px, ${ty}px) scale(${scale})`,
            transition: animate ? 'transform 0.2s cubic-bezier(0.2, 0, 0, 1)' : 'none'
          }"
          draggable="false"
          @load="measure"
        />
      </div>
    </div>
  </div>
</template>

<style scoped>
.image-viewer-backdrop {
  position: fixed;
  inset: 0;
  z-index: 1500;
  display: flex;
  flex-direction: column;
  backdrop-filter: blur(8px);
  -webkit-backdrop-filter: blur(8px);
  transition: background-color 0.15s ease-out;
  touch-action: none;
  user-select: none;
  animation: viewer-fade-in 0.18s ease-out;
}

@keyframes viewer-fade-in {
  from {
    opacity: 0;
  }
  to {
    opacity: 1;
  }
}

.image-viewer-header {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  z-index: 10;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: max(12px, env(safe-area-inset-top, 12px)) max(16px, env(safe-area-inset-right, 16px)) 12px max(16px, env(safe-area-inset-left, 16px));
  background: linear-gradient(to bottom, rgba(0, 0, 0, 0.75) 0%, rgba(0, 0, 0, 0) 100%);
  pointer-events: auto;
}

.image-viewer-info {
  display: flex;
  align-items: baseline;
  gap: 8px;
  overflow: hidden;
  max-width: 50%;
  color: #fff;
}

.image-viewer-filename {
  font-size: 0.9rem;
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  text-shadow: 0 1px 2px rgba(0, 0, 0, 0.6);
}

.image-viewer-host {
  font-size: 0.75rem;
  color: rgba(255, 255, 255, 0.7);
  white-space: nowrap;
}

.image-viewer-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.image-viewer-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 44px;
  min-height: 44px;
  padding: 8px;
  background: rgba(30, 34, 40, 0.75);
  color: #e6edf3;
  border: 1px solid rgba(255, 255, 255, 0.15);
  border-radius: 8px;
  cursor: pointer;
  text-decoration: none;
  font-size: 1.1rem;
  line-height: 1;
  transition: background 0.15s ease, color 0.15s ease, border-color 0.15s ease;
  backdrop-filter: blur(4px);
  -webkit-backdrop-filter: blur(4px);
}

.image-viewer-btn:hover {
  background: rgba(45, 52, 60, 0.9);
  color: #fff;
  border-color: rgba(255, 255, 255, 0.3);
}

.image-viewer-btn.close {
  font-size: 1.25rem;
  font-weight: 500;
  background: rgba(220, 53, 69, 0.3);
  border-color: rgba(220, 53, 69, 0.5);
}

.image-viewer-btn.close:hover {
  background: rgba(220, 53, 69, 0.6);
  color: #fff;
}

.image-viewer-body {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  width: 100%;
  height: 100%;
  padding: 60px 16px 24px;
  box-sizing: border-box;
}

.image-viewer-btn:disabled {
  opacity: 0.4;
  cursor: default;
  pointer-events: none;
}

.image-viewer-zoom {
  display: flex;
  align-items: center;
  gap: 4px;
}

.image-viewer-zoom-level {
  min-width: 64px;
  font-size: 0.8rem;
  font-variant-numeric: tabular-nums;
}

.image-viewer-content {
  display: flex;
  align-items: center;
  justify-content: center;
  max-width: 100%;
  max-height: 100%;
  touch-action: none;
}

.image-viewer-img {
  display: block;
  max-width: 92vw;
  max-height: calc(100dvh - 120px);
  object-fit: contain;
  border-radius: 4px;
  cursor: zoom-in;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.5);
  transform-origin: center center;
  will-change: transform;
  -webkit-user-drag: none;
}

.image-viewer-img.zoomed {
  cursor: grab;
}

.image-viewer-img.zoomed:active {
  cursor: grabbing;
}

@media (max-width: 720px) {
  .image-viewer-header {
    padding-top: max(8px, env(safe-area-inset-top, 8px));
    padding-right: max(8px, env(safe-area-inset-right, 8px));
  }
  .image-viewer-filename {
    display: none;
  }
  .image-viewer-host {
    font-size: 0.8rem;
  }
  .image-viewer-btn {
    min-width: 44px;
    min-height: 44px;
  }
  /* Pinch is the primary zoom on phones; keep only the level/reset button. */
  .image-viewer-zoom .image-viewer-btn:not(.image-viewer-zoom-level) {
    display: none;
  }
  .image-viewer-img {
    max-width: 98vw;
    max-height: calc(100dvh - 100px);
  }
}
</style>
