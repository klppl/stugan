<script setup lang="ts">
import { ref } from "vue";
import { connection } from "../../connection";

const exportFormat = ref<"tar.gz" | "zip" | "db">("tar.gz");
const exporting = ref(false);

const importMode = ref<"replace" | "merge">("replace");
const selectedFile = ref<File | null>(null);
const fileInput = ref<HTMLInputElement | null>(null);
const isDragging = ref(false);
const importing = ref(false);
const importMsg = ref("");

async function handleExport() {
  if (exporting.value) return;
  exporting.value = true;
  try {
    const res = await fetch(`/api/export?format=${exportFormat.value}`);
    if (!res.ok) {
      throw new Error(`Export failed: ${res.statusText}`);
    }
    const blob = await res.blob();
    const disposition = res.headers.get("Content-Disposition") || "";
    let filename = `stugan-backup.${exportFormat.value}`;
    const match = disposition.match(/filename="?([^"]+)"?/);
    if (match && match[1]) {
      filename = match[1];
    }
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
    connection.showToast(`Exported backup (${filename})`, "system");
  } catch (err: any) {
    connection.showToast(`Export failed: ${err.message}`, "system");
  } finally {
    exporting.value = false;
  }
}

function onFileSelect(e: Event) {
  const target = e.target as HTMLInputElement;
  if (target.files && target.files.length > 0) {
    selectedFile.value = target.files[0];
    importMsg.value = "";
  }
}

function onDrop(e: DragEvent) {
  isDragging.value = false;
  if (e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files.length > 0) {
    selectedFile.value = e.dataTransfer.files[0];
    importMsg.value = "";
  }
}

function clearSelectedFile() {
  selectedFile.value = null;
  if (fileInput.value) fileInput.value.value = "";
  importMsg.value = "";
}

async function handleImport() {
  if (!selectedFile.value || importing.value) return;

  if (importMode.value === "replace") {
    const ok = window.confirm(
      "Are you sure you want to restore this backup in REPLACE mode?\n\nExisting messages, networks, and channel history will be overwritten with the contents of the backup."
    );
    if (!ok) return;
  }

  importing.value = true;
  importMsg.value = "Importing backup…";

  try {
    const form = new FormData();
    form.append("file", selectedFile.value);
    form.append("mode", importMode.value);

    const res = await fetch(`/api/import?mode=${importMode.value}`, {
      method: "POST",
      body: form,
    });

    if (!res.ok) {
      const errText = await res.text();
      throw new Error(errText || res.statusText);
    }

    const data = await res.json();
    importMsg.value = `✓ ${data.message || "Backup restored successfully"}`;
    connection.showToast("Backup restored successfully", "system");
    clearSelectedFile();
  } catch (err: any) {
    importMsg.value = `Failed: ${err.message}`;
    connection.showToast(`Import error: ${err.message}`, "system");
  } finally {
    importing.value = false;
  }
}

function fmtSize(bytes: number): string {
  if (bytes >= 1 << 20) return (bytes / (1 << 20)).toFixed(1) + " MB";
  if (bytes >= 1 << 10) return (bytes / (1 << 10)).toFixed(1) + " kB";
  return bytes + " B";
}
</script>

<template>
  <div class="settings-section">
    <div class="section-header">
      <h3 class="section-title">💾 Backup & Data Portability</h3>
      <p class="section-desc">
        Export and import portable full backups of your chat history, networks, plugins, and preferences.
      </p>
    </div>

    <!-- Export Card -->
    <div class="settings-card">
      <h4 class="card-subtitle">Export Backup</h4>
      <p class="hint">
        Generate a complete portable archive of your SQLite database, custom Lua plugins, and configuration settings.
      </p>

      <div class="setting-row">
        <div class="setting-label">
          <span>Archive Format</span>
          <span class="setting-hint">Choose how the backup bundle is packaged</span>
        </div>
        <select v-model="exportFormat" class="select-input">
          <option value="tar.gz">Compressed Archive (.tar.gz) - Recommended</option>
          <option value="zip">Zip Archive (.zip)</option>
          <option value="db">Raw SQLite Database (.db)</option>
        </select>
      </div>

      <div class="export-actions">
        <button class="btn btn-primary" :disabled="exporting" @click="handleExport">
          {{ exporting ? "Generating Backup…" : "Export & Download" }}
        </button>
      </div>
    </div>

    <!-- Import Card -->
    <div class="settings-card">
      <h4 class="card-subtitle">Import / Restore Backup</h4>
      <p class="hint">
        Restore a previously exported backup archive (.tar.gz, .zip, or .db).
      </p>

      <div class="setting-row">
        <div class="setting-label">
          <span>Restore Mode</span>
          <span class="setting-hint">Choose how the data is applied to your account</span>
        </div>
        <div class="mode-options">
          <label class="radio-label">
            <input type="radio" v-model="importMode" value="replace" />
            <span><strong>Replace</strong> (Full Restore - overwrites current database)</span>
          </label>
          <label class="radio-label">
            <input type="radio" v-model="importMode" value="merge" />
            <span><strong>Merge</strong> (Combine messages & networks with existing data)</span>
          </label>
        </div>
      </div>

      <!-- Dropzone -->
      <div
        class="import-dropzone"
        :class="{ dragging: isDragging, 'has-file': !!selectedFile }"
        @dragover.prevent="isDragging = true"
        @dragleave.prevent="isDragging = false"
        @drop.prevent="onDrop"
        @click="fileInput?.click()"
      >
        <input
          ref="fileInput"
          type="file"
          accept=".tar.gz,.tgz,.zip,.db,.sqlite,.sqlite3"
          class="file-input-hidden"
          @change="onFileSelect"
        />

        <div v-if="!selectedFile" class="dropzone-prompt">
          <div class="dropzone-icon">📦</div>
          <div class="dropzone-text">
            <strong>Click to select a backup file</strong> or drag and drop here
          </div>
          <span class="dropzone-sub">Supported formats: .tar.gz, .zip, .db</span>
        </div>

        <div v-else class="dropzone-file">
          <div class="file-info">
            <span class="file-name">📄 {{ selectedFile.name }}</span>
            <span class="file-size">({{ fmtSize(selectedFile.size) }})</span>
          </div>
          <button
            type="button"
            class="btn-danger-ghost btn-sm"
            @click.stop="clearSelectedFile"
          >
            Remove
          </button>
        </div>
      </div>

      <div v-if="importMsg" class="import-status" :class="{ 'status-err': importMsg.startsWith('Failed') }">
        {{ importMsg }}
      </div>

      <div class="import-actions">
        <button
          class="btn btn-primary"
          :disabled="!selectedFile || importing"
          @click="handleImport"
        >
          {{ importing ? "Restoring Backup…" : "Restore Backup" }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.export-actions,
.import-actions {
  margin-top: 1rem;
  display: flex;
  justify-content: flex-end;
}

.mode-options {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.radio-label {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.875rem;
  cursor: pointer;
}

.import-dropzone {
  margin-top: 1rem;
  padding: 1.5rem;
  border: 2px dashed var(--color-border, #333);
  border-radius: 8px;
  text-align: center;
  cursor: pointer;
  background: var(--color-bg-secondary, rgba(255, 255, 255, 0.02));
  transition: all 0.2s ease;
}

.import-dropzone:hover,
.import-dropzone.dragging {
  border-color: var(--color-accent, #6366f1);
  background: var(--color-bg-hover, rgba(99, 102, 241, 0.05));
}

.import-dropzone.has-file {
  border-style: solid;
  cursor: default;
}

.file-input-hidden {
  display: none;
}

.dropzone-prompt {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 0.5rem;
}

.dropzone-icon {
  font-size: 2rem;
}

.dropzone-text {
  font-size: 0.9rem;
}

.dropzone-sub {
  font-size: 0.75rem;
  opacity: 0.6;
}

.dropzone-file {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.file-info {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  font-size: 0.9rem;
}

.file-size {
  opacity: 0.6;
  font-size: 0.8rem;
}

.import-status {
  margin-top: 0.75rem;
  font-size: 0.875rem;
  color: var(--color-success, #22c55e);
}

.import-status.status-err {
  color: var(--color-danger, #ef4444);
}

.select-input {
  background: var(--color-bg-tertiary, #222);
  color: inherit;
  border: 1px solid var(--color-border, #444);
  border-radius: 4px;
  padding: 0.4rem 0.6rem;
  font-size: 0.875rem;
}
</style>
