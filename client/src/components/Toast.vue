<script setup lang="ts">
import { connection, type ToastLevel } from "../connection";
import Icon, { type IconName } from "./Icon.vue";

const store = connection.store;

function toastIcon(level?: ToastLevel): IconName {
  switch (level) {
    case "success":
      return "check";
    case "info":
      return "info";
    case "warning":
      return "alert-circle";
    case "error":
    default:
      return "x";
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
      <Icon :name="toastIcon(t.level)" class="toast-icon" />
      <span class="toast-msg">{{ t.message }}</span>
      <span
        v-if="t.code && t.code !== 'success' && t.code !== 'info' && t.code !== 'error'"
        class="toast-code"
      >{{ t.code }}</span>
    </div>
  </div>
</template>
