<script setup lang="ts">
import { ref, onMounted } from "vue";
import { connection } from "../../connection";
import type { UploadEntry } from "../../connection";

const hasUploads = connection.hasCap("uploads");
const uploadList = ref<UploadEntry[] | null>(null);
const deleting = ref<Record<string, boolean>>({});

onMounted(async () => {
  if (hasUploads) uploadList.value = await connection.listUploads();
});

function fmtSize(n: number): string {
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + " MB";
  if (n >= 1 << 10) return (n / (1 << 10)).toFixed(1) + " kB";
  return n + " B";
}

function expiresIn(iso: string): string {
  const ms = new Date(iso).getTime() - Date.now();
  if (ms <= 0) return "soon";
  const hours = Math.round(ms / 3_600_000);
  if (hours < 48) return `in ${hours} hour${hours === 1 ? "" : "s"}`;
  const days = Math.round(hours / 24);
  return `in ${days} days`;
}

async function handleDelete(u: UploadEntry) {
  const key = u.id || u.url;
  if (!key || deleting.value[key]) return;
  deleting.value[key] = true;
  const ok = await connection.deleteUpload(key);
  delete deleting.value[key];
  if (ok) {
    if (uploadList.value) {
      uploadList.value = uploadList.value.filter((x) => x !== u && (x.id || x.url) !== key);
    }
    connection.showToast(`Deleted ${u.name || "upload"}`, "upload");
  } else {
    connection.showToast(`Failed to delete ${u.name || "upload"}`, "upload");
  }
}
</script>

<template>
  <div class="settings-section">
    <div class="section-header">
      <h3 class="section-title">📁 Stored Uploads</h3>
      <p class="section-desc">Manage files uploaded to the server and view retention expiry schedules.</p>
    </div>

    <div v-if="!hasUploads" class="settings-card">
      <p class="hint">The connected server does not have file storage / upload directory enabled.</p>
    </div>

    <div v-else class="settings-card">
      <p class="hint">
        Uploaded media and files are automatically retained for 3 to 7 days depending on file size (larger files expire sooner).
      </p>

      <div v-if="uploadList === null" class="loading-state">
        <p class="hint">Loading upload list…</p>
      </div>

      <div v-else-if="!uploadList.length" class="empty-state">
        <p class="hint">No active stored uploads found for your user account.</p>
      </div>

      <div v-else class="uploads-list">
        <div v-for="u in uploadList" :key="u.id || u.url" class="upload-row">
          <a :href="u.url" target="_blank" rel="noopener" class="upload-link">
            📄 {{ u.name || u.url.slice(u.url.lastIndexOf("/") + 1) }}
          </a>
          <div class="upload-meta">
            <span class="upload-size">{{ fmtSize(u.size) }}</span>
            <span class="upload-expiry">expires {{ expiresIn(u.expires) }}</span>
            <button
              type="button"
              class="btn-danger-ghost upload-delete-btn"
              :disabled="deleting[u.id || u.url]"
              @click="handleDelete(u)"
              title="Delete upload"
            >
              {{ deleting[u.id || u.url] ? "Deleting…" : "Delete" }}
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
