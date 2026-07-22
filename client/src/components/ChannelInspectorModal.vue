<script setup lang="ts">
import { computed } from "vue";
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
</script>

<template>
  <div class="settings-overlay" @click.self="emit('close')">
    <div class="settings inspector-modal">
      <div class="inspector-header">
        <h2>{{ channel.name }}</h2>
        <span class="channel-kind-badge">{{ channel.kind }}</span>
      </div>

      <div class="inspector-section">
        <h3>Topic</h3>
        <p class="inspector-topic">{{ channel.topic || "(No topic set)" }}</p>
        <div v-if="channel.topic_setter" class="topic-meta">
          Set by <span class="topic-setter">{{ channel.topic_setter }}</span>
          <span v-if="topicTimeFormatted"> on {{ topicTimeFormatted }}</span>
        </div>
      </div>

      <div v-if="channel.kind === 'channel'" class="inspector-section">
        <h3>Channel Modes</h3>
        <p v-if="!channel.mode" class="hint">No specific channel modes active</p>
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
      </div>

      <div v-if="channel.kind === 'channel'" class="inspector-section">
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
      </div>

      <div class="row inspector-actions">
        <button type="button" @click="promptTopic">Set Topic</button>
        <button v-if="channel.kind === 'channel'" type="button" class="danger" @click="partChannel">
          Leave Channel
        </button>
        <span class="spacer"></span>
        <button type="button" @click="emit('close')">Close</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.inspector-modal {
  max-width: 520px;
}
.inspector-header {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  margin-bottom: 1rem;
}
.inspector-header h2 {
  margin: 0;
  word-break: break-all;
}
.channel-kind-badge {
  background: var(--bg-surface-2, rgba(255, 255, 255, 0.1));
  border-radius: 4px;
  padding: 0.15rem 0.5rem;
  font-size: 0.75rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--fg-muted);
}
.inspector-section {
  margin-bottom: 1.25rem;
}
.inspector-section h3 {
  font-size: 0.85rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--fg-muted);
  margin: 0 0 0.5rem 0;
}
.inspector-topic {
  margin: 0;
  font-size: 0.95rem;
  line-height: 1.4;
  white-space: pre-wrap;
  word-break: break-word;
}
.topic-meta {
  font-size: 0.8rem;
  color: var(--fg-muted);
  margin-top: 0.35rem;
}
.topic-setter {
  font-weight: 600;
  color: var(--fg);
}
.mode-badges {
  display: flex;
  flex-wrap: wrap;
  gap: 0.4rem;
  align-items: center;
}
.mode-raw {
  background: var(--accent, #5865f2);
  color: #fff;
  font-weight: 700;
  font-family: monospace;
  padding: 0.2rem 0.5rem;
  border-radius: 4px;
  font-size: 0.85rem;
}
.mode-badge {
  background: var(--bg-surface-2, rgba(255, 255, 255, 0.08));
  border: 1px solid var(--border, rgba(255, 255, 255, 0.12));
  padding: 0.2rem 0.5rem;
  border-radius: 4px;
  font-size: 0.8rem;
}
.member-stats-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 0.5rem;
}
.stat-box {
  background: var(--bg-surface-2, rgba(255, 255, 255, 0.05));
  border: 1px solid var(--border, rgba(255, 255, 255, 0.1));
  border-radius: 6px;
  padding: 0.5rem;
  text-align: center;
}
.stat-num {
  display: block;
  font-size: 1.25rem;
  font-weight: 700;
}
.stat-label {
  font-size: 0.75rem;
  color: var(--fg-muted);
}
.inspector-actions {
  margin-top: 1.5rem;
}
</style>
