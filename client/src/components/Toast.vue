<script setup lang="ts">
import { connection } from "../connection";

const store = connection.store;
</script>

<template>
  <!-- Corner overlay for transient server notices (s2c `error` frames).
       Click a toast to dismiss it early; otherwise it auto-clears after a
       few seconds (see Connection.pushToast). -->
  <div class="toasts" aria-live="polite">
    <div
      v-for="t in store.toasts"
      :key="t.id"
      class="toast"
      role="status"
      @click="connection.dismissToast(t.id)"
    >
      <span class="toast-msg">{{ t.message }}</span>
      <span v-if="t.code" class="toast-code">{{ t.code }}</span>
    </div>
  </div>
</template>
