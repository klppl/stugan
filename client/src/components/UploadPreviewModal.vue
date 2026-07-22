<script setup lang="ts">
import { computed, ref, onUnmounted } from "vue";
import { connection } from "../connection";

const props = defineProps<{
  file: File;
  network: string;
  buffer: string;
}>();

const emit = defineEmits<{ close: [] }>();

const comment = ref("");
const uploading = ref(false);
const errorMsg = ref("");

const previewUrl = computed(() => {
  return URL.createObjectURL(props.file);
});

onUnmounted(() => {
  if (previewUrl.value) {
    URL.revokeObjectURL(previewUrl.value);
  }
});

const formattedSize = computed(() => {
  const bytes = props.file.size;
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
});

async function handleUpload() {
  if (uploading.value) return;
  uploading.value = true;
  errorMsg.value = "";
  try {
    const url = await connection.upload(props.file);
    if (!url) {
      errorMsg.value = "Upload failed. Check maximum upload size or server configuration.";
      uploading.value = false;
      return;
    }
    const text = comment.value.trim() ? `${comment.value.trim()} ${url}` : url;
    connection.send(props.network, props.buffer, text);
    emit("close");
  } catch (err: any) {
    errorMsg.value = err?.message || "Upload failed";
    uploading.value = false;
  }
}
</script>

<template>
  <div class="settings-overlay" @click.self="emit('close')">
    <div class="settings upload-preview-modal">
      <h2>Upload Image Preview</h2>
      <p class="hint">Previewing clipboard image before sending to <code>{{ buffer }}</code></p>

      <div class="preview-container">
        <img :src="previewUrl" :alt="file.name" class="preview-img" />
      </div>

      <div class="file-meta">
        <span class="file-name">{{ file.name || "clipboard-image.png" }}</span>
        <span class="file-size">{{ formattedSize }}</span>
      </div>

      <label class="row">
        <span>Comment</span>
        <input
          v-model="comment"
          type="text"
          placeholder="Add an optional comment…"
          autofocus
          @keydown.enter.prevent="handleUpload"
        />
      </label>

      <div v-if="errorMsg" class="upload-error">{{ errorMsg }}</div>

      <div class="row preview-actions">
        <button type="button" @click="emit('close')" :disabled="uploading">Cancel</button>
        <span class="spacer"></span>
        <button type="button" class="primary" @click="handleUpload" :disabled="uploading">
          {{ uploading ? "Uploading…" : "Upload & Send" }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.upload-preview-modal {
  max-width: 480px;
}
.preview-container {
  display: flex;
  justify-content: center;
  align-items: center;
  background: var(--bg-surface-2, rgba(0, 0, 0, 0.2));
  border: 1px dashed var(--border);
  border-radius: 8px;
  padding: 1rem;
  margin: 1rem 0;
  max-height: 300px;
  overflow: hidden;
}
.preview-img {
  max-width: 100%;
  max-height: 260px;
  object-fit: contain;
  border-radius: 4px;
}
.file-meta {
  display: flex;
  justify-content: space-between;
  font-size: 0.85rem;
  color: var(--fg-muted);
  margin-bottom: 1rem;
}
.file-name {
  font-weight: 600;
  color: var(--fg);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.upload-error {
  color: var(--hl, #ff6b6b);
  font-size: 0.85rem;
  margin-top: 0.5rem;
}
.preview-actions {
  margin-top: 1.25rem;
}
</style>
