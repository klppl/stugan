<script setup lang="ts">
import { connection, type ToastLevel } from "../connection";

const store = connection.store;

function toastIcon(level?: ToastLevel) {
  switch (level) {
    case "success":
      return "✓";
    case "info":
      return "ℹ";
    case "warning":
      return "⚠";
    case "error":
    default:
      return "✕";
  }
}
</script>

<template>
  <!-- Corner overlay for transient server notices and user action feedback.
       Click a toast to dismiss it early; otherwise it auto-clears after a
       few seconds (see Connection.showToast). -->
  <div class="toasts" aria-live="polite">
    <div
      v-for="t in store.toasts"
      :key="t.id"
      :class="['toast', `toast-${t.level || 'error'}`]"
      role="status"
      @click="connection.dismissToast(t.id)"
    >
      <span class="toast-icon" aria-hidden="true">{{ toastIcon(t.level) }}</span>
      <span class="toast-msg">{{ t.message }}</span>
      <span
        v-if="t.code && t.code !== 'success' && t.code !== 'info' && t.code !== 'error'"
        class="toast-code"
      >{{ t.code }}</span>
    </div>
  </div>
</template>
