<script setup lang="ts">
import { ref } from "vue";
import { connection, bufKey, type Buffer } from "../connection";
import { isMuted, toggleMute } from "../settings";
import { useContextMenu } from "../contextMenu";
import { ui, closeDrawers } from "../ui";
import AddNetwork from "./AddNetwork.vue";
import EncryptionKey from "./EncryptionKey.vue";
import NetworkSettings from "./NetworkSettings.vue";

const store = connection.store;
const showAdd = ref(false);
const settingsFor = ref<string | null>(null);
const keyDialogFor = ref<{ network: string; buffer: string } | null>(null);

const ctx = useContextMenu<{ network: string; buffer: string }>({ height: 140 });

function isActive(network: string, buffer: string): boolean {
  return store.view === "chat" && store.active?.network === network && store.active?.buffer === buffer;
}

function selectBuffer(network: string, buffer: string) {
  // A long-press-triggered open already happened — suppress the
  // would-be tap-to-select that fires when the finger lifts.
  if (ctx.shouldSuppressClick()) return;
  connection.select(network, buffer);
  // On mobile the sidebar is a drawer — collapse it so the user lands in chat.
  if (ui.isMobile) closeDrawers();
}

function isEncrypted(buf: Buffer): boolean {
  return !!buf.state?.encrypted;
}

function muteFromMenu() {
  const p = ctx.state.value?.payload;
  if (!p) return;
  toggleMute(bufKey(p.network, p.buffer));
  ctx.close();
}
function keyFromMenu() {
  const p = ctx.state.value?.payload;
  if (!p) return;
  keyDialogFor.value = { network: p.network, buffer: p.buffer };
  ctx.close();
}
</script>

<template>
  <nav class="sidebar" :class="{ open: ui.sidebarOpen }">
    <div class="brand">stugan</div>

    <div v-for="net in store.networks" :key="net.id" class="network">
      <div class="network-name" @click="settingsFor = net.id" title="network settings">
        <span>{{ net.name }} <span class="nick">({{ net.nick }})</span></span>
        <span class="net-gear" @click.stop="settingsFor = net.id">⚙</span>
      </div>
      <ul class="buffers">
        <li
          v-for="buf in net.buffers"
          :key="buf.name"
          :class="{ active: isActive(net.id, buf.name), [buf.kind]: true, muted: isMuted(bufKey(net.id, buf.name)) }"
          @click="selectBuffer(net.id, buf.name)"
          @contextmenu="ctx.onContext({ network: net.id, buffer: buf.name }, $event)"
          @touchstart.passive="ctx.onTouchStart({ network: net.id, buffer: buf.name }, $event)"
          @touchmove.passive="ctx.onTouchMove($event)"
          @touchend="ctx.cancelLp"
          @touchcancel="ctx.cancelLp"
          title="right-click (or long-press) for buffer options"
        >
          <span v-if="isEncrypted(buf)" class="lock-icon" :title="`encrypted (${buf.state.encrypted})`">🔒</span>
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
    <NetworkSettings v-if="settingsFor" :network="settingsFor" @close="settingsFor = null" />
    <EncryptionKey
      v-if="keyDialogFor"
      :network="keyDialogFor.network"
      :buffer="keyDialogFor.buffer"
      @close="keyDialogFor = null"
    />

    <div
      v-if="ctx.state.value"
      class="ctx-menu"
      :style="{ left: ctx.state.value.x + 'px', top: ctx.state.value.y + 'px' }"
      role="menu"
    >
      <div class="ctx-header">{{ ctx.state.value.payload.buffer }}</div>
      <button class="ctx-item" type="button" @click="muteFromMenu">
        {{ isMuted(bufKey(ctx.state.value.payload.network, ctx.state.value.payload.buffer)) ? "Unmute" : "Mute" }}
      </button>
      <button class="ctx-item" type="button" @click="keyFromMenu">
        Set encryption key…
      </button>
    </div>
  </nav>
</template>
