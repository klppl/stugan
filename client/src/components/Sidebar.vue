<script setup lang="ts">
import { connection } from "../connection";

const store = connection.store;

function isActive(network: string, buffer: string): boolean {
  return (
    store.active?.network === network && store.active?.buffer === buffer
  );
}
</script>

<template>
  <nav class="sidebar">
    <div class="brand">stugan</div>
    <div class="conn-status" :class="store.status">{{ store.status }}</div>

    <div v-for="net in store.networks" :key="net.id" class="network">
      <div class="network-name">
        {{ net.name }}
        <span class="nick">({{ net.nick }})</span>
      </div>
      <ul class="buffers">
        <li
          v-for="buf in net.buffers"
          :key="buf.name"
          :class="{ active: isActive(net.id, buf.name), [buf.kind]: true }"
          @click="connection.select(net.id, buf.name)"
        >
          {{ buf.name }}
        </li>
      </ul>
    </div>
  </nav>
</template>
