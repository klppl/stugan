<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import type { ActiveImage } from "../ui";
import { connection } from "../connection";

const props = defineProps<{
  image: ActiveImage;
}>();

const emit = defineEmits<{
  close: [];
}>();

const isZoomed = ref(false);
const isDragging = ref(false);
const dragOffsetY = ref(0);
let touchStartY = 0;
let closed = false;

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
  }
}

function toggleZoom() {
  isZoomed.value = !isZoomed.value;
}

async function copyLink() {
  try {
    await navigator.clipboard.writeText(props.image.url);
    connection.showToast("Image link copied to clipboard", undefined, "info");
  } catch {
    connection.showToast("Failed to copy link", undefined, "error");
  }
}

// Touch swipe-to-dismiss
function onTouchStart(e: TouchEvent) {
  if (e.touches.length !== 1 || isZoomed.value) return;
  touchStartY = e.touches[0].clientY;
  isDragging.value = true;
  dragOffsetY.value = 0;
}

function onTouchMove(e: TouchEvent) {
  if (!isDragging.value || e.touches.length !== 1) return;
  const currentY = e.touches[0].clientY;
  dragOffsetY.value = currentY - touchStartY;
}

function onTouchEnd() {
  if (!isDragging.value) return;
  isDragging.value = false;
  // If dragged far enough (> 70px), dismiss modal; otherwise spring back.
  if (Math.abs(dragOffsetY.value) > 70) {
    dismiss();
  } else {
    dragOffsetY.value = 0;
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
  }
});

onUnmounted(() => {
  if (typeof window !== "undefined") {
    window.removeEventListener("popstate", onPopState);
    window.removeEventListener("keydown", onKeydown);
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
        <button
          type="button"
          class="image-viewer-btn"
          :title="isZoomed ? 'Fit to screen' : 'Zoom to original size'"
          :aria-label="isZoomed ? 'Fit to screen' : 'Zoom to original size'"
          @click="toggleZoom"
        >
          <svg v-if="!isZoomed" aria-hidden="true" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
            <circle cx="11" cy="11" r="8" />
            <line x1="21" y1="21" x2="16.65" y2="16.65" />
            <line x1="11" y1="8" x2="11" y2="14" />
            <line x1="8" y1="11" x2="14" y2="11" />
          </svg>
          <svg v-else aria-hidden="true" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2">
            <circle cx="11" cy="11" r="8" />
            <line x1="21" y1="21" x2="16.65" y2="16.65" />
            <line x1="8" y1="11" x2="14" y2="11" />
          </svg>
        </button>

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
          ✕
        </button>
      </div>
    </header>

    <!-- Content area -->
    <div
      class="image-viewer-body"
      :class="{ 'is-zoomed': isZoomed }"
      @click.self="dismiss()"
    >
      <div
        class="image-viewer-content"
        :style="{
          transform: `translateY(${dragOffsetY}px)`,
          transition: isDragging ? 'none' : 'transform 0.2s cubic-bezier(0.2, 0, 0, 1)'
        }"
        @touchstart="onTouchStart"
        @touchmove="onTouchMove"
        @touchend="onTouchEnd"
      >
        <img
          :src="image.proxiedUrl"
          :alt="image.alt || fileName || 'Image'"
          class="image-viewer-img"
          :class="{ 'zoomed': isZoomed }"
          @click="toggleZoom"
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

.image-viewer-body.is-zoomed {
  overflow: auto;
  align-items: flex-start;
  justify-content: flex-start;
  touch-action: pan-x pan-y;
}

.image-viewer-content {
  display: flex;
  align-items: center;
  justify-content: center;
  max-width: 100%;
  max-height: 100%;
}

.image-viewer-body.is-zoomed .image-viewer-content {
  margin: auto;
  max-width: none;
  max-height: none;
}

.image-viewer-img {
  display: block;
  max-width: 92vw;
  max-height: calc(100dvh - 120px);
  object-fit: contain;
  border-radius: 4px;
  cursor: zoom-in;
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.5);
}

.image-viewer-img.zoomed {
  max-width: none;
  max-height: none;
  cursor: zoom-out;
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
  .image-viewer-img {
    max-width: 98vw;
    max-height: calc(100dvh - 100px);
  }
}
</style>
