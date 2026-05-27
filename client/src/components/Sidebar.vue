<script setup lang="ts">
import { ref } from "vue";
import { connection, bufKey } from "../connection";
import { isMuted, toggleMute } from "../settings";
import AddNetwork from "./AddNetwork.vue";

const store = connection.store;
const showAdd = ref(false);

function isActive(network: string, buffer: string): boolean {
  return store.view === "chat" && store.active?.network === network && store.active?.buffer === buffer;
}

function removeNetwork(id: string, name: string) {
  if (confirm(`Remove network "${name}"? This disconnects and forgets it.`)) {
    connection.removeNetwork(id);
  }
}
</script>

<template>
  <nav class="sidebar">
    <div class="brand">stugan</div>

    <div v-for="net in store.networks" :key="net.id" class="network">
      <div class="network-name" @contextmenu.prevent="removeNetwork(net.id, net.name)" title="right-click to remove">
        {{ net.name }}
        <span class="nick">({{ net.nick }})</span>
      </div>
      <ul class="buffers">
        <li
          v-for="buf in net.buffers"
          :key="buf.name"
          :class="{ active: isActive(net.id, buf.name), [buf.kind]: true, muted: isMuted(bufKey(net.id, buf.name)) }"
          @click="connection.select(net.id, buf.name)"
          @contextmenu.prevent="toggleMute(bufKey(net.id, buf.name))"
          :title="isMuted(bufKey(net.id, buf.name)) ? 'muted — right-click to unmute' : 'right-click to mute'"
        >
          <span class="buf-name">{{ buf.name }}</span>
          <span v-if="isMuted(bufKey(net.id, buf.name))" class="mute-icon">🔇</span>
          <span
            v-else-if="buf.unread > 0 && !isActive(net.id, buf.name)"
            class="badge"
            :class="{ highlight: buf.highlight > 0 }"
            >{{ buf.unread }}</span
          >
        </li>
      </ul>
    </div>

    <button class="add-network" @click="showAdd = true">+ Add network</button>
    <AddNetwork v-if="showAdd" @close="showAdd = false" />
  </nav>
</template>
