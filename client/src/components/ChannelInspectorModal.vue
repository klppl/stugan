<script setup lang="ts">
import { computed, onMounted, onUnmounted } from "vue";
import { connection } from "../connection";
import type { ChannelDTO } from "../proto/events";

const props = defineProps<{
  network: string;
  channel: ChannelDTO;
}>();

const emit = defineEmits<{ close: [] }>();

const topicTimeFormatted = computed(() => {
  if (!props.channel.topic_time) return "";
  try {
    const d = new Date(props.channel.topic_time);
    return d.toLocaleString();
  } catch {
    return props.channel.topic_time;
  }
});

const memberStats = computed(() => {
  const members = props.channel.members || [];
  let ops = 0;
  let voiced = 0;
  let normal = 0;
  for (const m of members) {
    if (m.modes.includes("@") || m.modes.includes("~") || m.modes.includes("&") || m.modes.includes("%")) {
      ops++;
    } else if (m.modes.includes("+")) {
      voiced++;
    } else {
      normal++;
    }
  }
  return { total: members.length, ops, voiced, normal };
});

const modeExplanations = computed(() => {
  const modes = props.channel.mode || "";
  const list: { flag: string; label: string }[] = [];
  if (modes.includes("n")) list.push({ flag: "+n", label: "No external messages" });
  if (modes.includes("t")) list.push({ flag: "+t", label: "Only ops can set topic" });
  if (modes.includes("k")) list.push({ flag: "+k", label: "Password key required" });
  if (modes.includes("i")) list.push({ flag: "+i", label: "Invite-only" });
  if (modes.includes("s")) list.push({ flag: "+s", label: "Secret channel" });
  if (modes.includes("m")) list.push({ flag: "+m", label: "Moderated (voiced only)" });
  if (modes.includes("p")) list.push({ flag: "+p", label: "Private channel" });
  if (modes.includes("l")) list.push({ flag: "+l", label: "User limit" });
  return list;
});

function partChannel() {
  connection.send(props.network, props.channel.name, `/part ${props.channel.name}`);
  emit("close");
}

function promptTopic() {
  const newTopic = prompt("Enter new channel topic:", props.channel.topic || "");
  if (newTopic !== null) {
    connection.send(props.network, props.channel.name, `/topic ${newTopic}`);
  }
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === "Escape") {
    emit("close");
  }
}

onMounted(() => window.addEventListener("keydown", onKeydown));
onUnmounted(() => window.removeEventListener("keydown", onKeydown));
</script>

<template>
  <div class="modal-backdrop" @click.self="emit('close')">
    <div class="modal-card">
      <header class="modal-header">
        <div class="header-title">
          <h2>{{ channel.name }}</h2>
          <span class="kind-badge">{{ channel.kind }}</span>
        </div>
        <button class="close-btn" aria-label="Close" @click="emit('close')">✕</button>
      </header>

      <div class="modal-body">
        <section class="info-section">
          <h3>Topic</h3>
          <div class="topic-box">
            <p class="topic-text">{{ channel.topic || "(No topic set)" }}</p>
            <div v-if="channel.topic_setter" class="topic-meta">
              Set by <strong class="setter">{{ channel.topic_setter }}</strong>
              <span v-if="topicTimeFormatted"> on {{ topicTimeFormatted }}</span>
            </div>
          </div>
        </section>

        <section v-if="channel.kind === 'channel'" class="info-section">
          <h3>Channel Modes</h3>
          <p v-if="!channel.mode" class="no-modes-hint">No specific channel modes active</p>
          <div v-else class="mode-badges">
            <span class="mode-raw">{{ channel.mode }}</span>
            <span
              v-for="m in modeExplanations"
              :key="m.flag"
              class="mode-badge"
              :title="m.label"
            >
              <strong>{{ m.flag }}</strong> {{ m.label }}
            </span>
          </div>
        </section>

        <section v-if="channel.kind === 'channel'" class="info-section">
          <h3>Members ({{ memberStats.total }})</h3>
          <div class="member-stats-grid">
            <div class="stat-box">
              <span class="stat-num">{{ memberStats.ops }}</span>
              <span class="stat-label">Operators</span>
            </div>
            <div class="stat-box">
              <span class="stat-num">{{ memberStats.voiced }}</span>
              <span class="stat-label">Voiced</span>
            </div>
            <div class="stat-box">
              <span class="stat-num">{{ memberStats.normal }}</span>
              <span class="stat-label">Members</span>
            </div>
          </div>
        </section>
      </div>

      <footer class="modal-footer">
        <button type="button" class="btn btn-sm" @click="promptTopic">✏ Set Topic</button>
        <button v-if="channel.kind === 'channel'" type="button" class="btn btn-sm btn-danger-ghost" @click="partChannel">
          Leave Channel
        </button>
        <span class="spacer" />
        <button type="button" class="btn btn-sm btn-primary" @click="emit('close')">Close</button>
      </footer>
    </div>
  </div>
</template>

<style scoped>
.modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.65);
  backdrop-filter: blur(4px);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
  padding: 16px;
}

.modal-card {
  background: var(--bg);
  color: var(--fg);
  border: 1px solid var(--border);
  border-radius: 12px;
  width: 520px;
  max-width: 100%;
  max-height: 85vh;
  display: flex;
  flex-direction: column;
  box-shadow: 0 20px 50px rgba(0, 0, 0, 0.45);
  overflow: hidden;
  animation: modal-pop 0.15s ease-out;
}

@keyframes modal-pop {
  from {
    opacity: 0;
    transform: scale(0.96);
  }
  to {
    opacity: 1;
    transform: scale(1);
  }
}

.modal-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 16px 20px;
  background: var(--bg-sidebar);
  border-bottom: 1px solid var(--border);
}

.header-title {
  display: flex;
  align-items: center;
  gap: 10px;
}

.header-title h2 {
  margin: 0;
  font-size: 18px;
  font-weight: 700;
  word-break: break-all;
}

.kind-badge {
  background: var(--bg-alt);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 2px 6px;
  font-size: 11px;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--fg-dim);
}

.close-btn {
  background: transparent;
  border: none;
  color: var(--fg-dim);
  font-size: 18px;
  cursor: pointer;
  padding: 4px 8px;
  border-radius: 4px;
}

.close-btn:hover {
  color: var(--fg);
  background: var(--bg-alt);
}

.modal-body {
  padding: 20px;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
  gap: 18px;
}

.info-section h3 {
  font-size: 12px;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--fg-dim);
  margin: 0 0 8px 0;
}

.topic-box {
  background: var(--bg-alt);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 12px 14px;
}

.topic-text {
  margin: 0;
  font-size: 14px;
  line-height: 1.45;
  white-space: pre-wrap;
  word-break: break-word;
}

.topic-meta {
  font-size: 12px;
  color: var(--fg-dim);
  margin-top: 6px;
}

.setter {
  color: var(--fg);
  font-weight: 600;
}

.no-modes-hint {
  font-size: 13px;
  color: var(--fg-dim);
  margin: 0;
}

.mode-badges {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  align-items: center;
}

.mode-raw {
  background: var(--accent);
  color: #fff;
  font-weight: 700;
  font-family: ui-monospace, monospace;
  padding: 3px 8px;
  border-radius: 4px;
  font-size: 13px;
}

.mode-badge {
  background: var(--bg-alt);
  border: 1px solid var(--border);
  padding: 3px 8px;
  border-radius: 4px;
  font-size: 12px;
}

.member-stats-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 8px;
}

.stat-box {
  background: var(--bg-alt);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 10px;
  text-align: center;
}

.stat-num {
  display: block;
  font-size: 20px;
  font-weight: 700;
  color: var(--accent);
}

.stat-label {
  font-size: 11px;
  color: var(--fg-dim);
  text-transform: uppercase;
}

.modal-footer {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 14px 20px;
  background: var(--bg-sidebar);
  border-top: 1px solid var(--border);
}

.spacer {
  flex: 1;
}

.btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 6px 12px;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--fg);
  font-size: 13px;
  cursor: pointer;
}

.btn:hover {
  border-color: var(--accent);
  background: var(--bg-alt);
}

.btn-primary {
  background: var(--accent);
  color: #fff;
  border-color: var(--accent);
}

.btn-danger-ghost {
  border-color: color-mix(in srgb, var(--hl) 40%, transparent);
  color: var(--hl);
  background: transparent;
}

.btn-danger-ghost:hover {
  background: color-mix(in srgb, var(--hl) 15%, transparent);
}
</style>
