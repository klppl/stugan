<script setup lang="ts">
import { computed, ref } from "vue";
import { connection, bufKey, type Buffer, type Network } from "../connection";
import { useContextMenu } from "../contextMenu";
import { ui, closeDrawers } from "../ui";
import AddNetwork from "./AddNetwork.vue";
import EncryptionKey from "./EncryptionKey.vue";
import NetworkSettings from "./NetworkSettings.vue";

const store = connection.store;
const showAdd = ref(false);
const settingsFor = ref<string | null>(null);
const keyDialogFor = ref<{ network: string; buffer: string } | null>(null);

const ctx = useContextMenu<{ network: string; buffer: string; kind: string }>({ height: 180 });

// Map the raw WebSocket state to a friendly label, shown in the footer pill.
const statusLabel = computed(
  () => ({ connecting: "connecting", open: "connected", closed: "disconnected" })[store.status],
);

// The per-network status buffer ("*status") is folded into the network
// header rather than shown as its own list item — clicking the network name
// opens it, while the gear (to the right) still opens network settings.
const STATUS = "*status";

function statusBuf(net: Network): Buffer | undefined {
  return net.buffers.find((b) => b.kind === "status");
}
function channelBuffers(net: Network): Buffer[] {
  return net.buffers.filter((b) => b.kind !== "status");
}

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
  connection.toggleMute(p.network, p.buffer);
  ctx.close();
}
function keyFromMenu() {
  const p = ctx.state.value?.payload;
  if (!p) return;
  keyDialogFor.value = { network: p.network, buffer: p.buffer };
  ctx.close();
}
function leaveFromMenu() {
  const p = ctx.state.value?.payload;
  if (!p) return;
  connection.send(p.network, p.buffer, "/part");
  ctx.close();
}
function closeFromMenu() {
  const p = ctx.state.value?.payload;
  if (!p) return;
  connection.closeBuffer(p.network, p.buffer);
  ctx.close();
}

// --- Drag-and-drop reordering (desktop) -----------------------------------
// Networks reorder among each other; buffers reorder only within their own
// network. Order is applied optimistically here, then sent to the server,
// which persists it and echoes back (net:reorder / net:update). Touch is not
// wired up — long-press already owns touch for the context menu.
type DragState =
  | { kind: "net"; id: string }
  | { kind: "buf"; network: string; name: string };

const drag = ref<DragState | null>(null);
// dropHint highlights the insertion edge of the row under the cursor.
const dropHint = ref<{ key: string; pos: "before" | "after" } | null>(null);

function netKey(net: Network): string {
  return `net:${net.id}`;
}
function bufDropKey(net: Network, buf: Buffer): string {
  return `buf:${net.id}:${buf.name}`;
}
function hintFor(key: string): "before" | "after" | null {
  return dropHint.value?.key === key ? dropHint.value.pos : null;
}

// dropPos reports whether the cursor is over the top or bottom half of the row.
function dropPos(e: DragEvent): "before" | "after" {
  const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
  return e.clientY < rect.top + rect.height / 2 ? "before" : "after";
}

function clearDrag() {
  drag.value = null;
  dropHint.value = null;
}

// moveBefore returns arr with the item at `from` reinserted relative to the
// item at the target index (recomputed after removal so before/after is exact).
function moveRelative<T>(arr: T[], from: number, targetIdx: number, pos: "before" | "after"): T[] {
  const copy = arr.slice();
  const [item] = copy.splice(from, 1);
  // targetIdx referred to the pre-removal array; if we removed an earlier
  // element the target shifted left by one.
  let t = targetIdx;
  if (from < targetIdx) t -= 1;
  copy.splice(pos === "before" ? t : t + 1, 0, item);
  return copy;
}

function onNetDragStart(net: Network, e: DragEvent) {
  drag.value = { kind: "net", id: net.id };
  e.dataTransfer?.setData("text/plain", net.id); // Firefox needs payload to drag
  if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
}

function onNetDragOver(net: Network, e: DragEvent) {
  if (drag.value?.kind !== "net" || drag.value.id === net.id) return;
  e.preventDefault();
  if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
  dropHint.value = { key: netKey(net), pos: dropPos(e) };
}

function onNetDrop(net: Network, e: DragEvent) {
  const d = drag.value;
  if (d?.kind !== "net" || d.id === net.id) return clearDrag();
  e.preventDefault();
  const nets = store.networks;
  const from = nets.findIndex((n) => n.id === d.id);
  const to = nets.findIndex((n) => n.id === net.id);
  if (from < 0 || to < 0) return clearDrag();
  const reordered = moveRelative(nets, from, to, dropPos(e));
  store.networks = reordered;
  connection.reorderNetworks(reordered.map((n) => n.id));
  clearDrag();
}

function onBufDragStart(net: Network, buf: Buffer, e: DragEvent) {
  drag.value = { kind: "buf", network: net.id, name: buf.name };
  e.dataTransfer?.setData("text/plain", buf.name);
  if (e.dataTransfer) e.dataTransfer.effectAllowed = "move";
}

function onBufDragOver(net: Network, buf: Buffer, e: DragEvent) {
  const d = drag.value;
  if (d?.kind !== "buf" || d.network !== net.id || d.name === buf.name) return;
  e.preventDefault();
  if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
  dropHint.value = { key: bufDropKey(net, buf), pos: dropPos(e) };
}

function onBufDrop(net: Network, buf: Buffer, e: DragEvent) {
  const d = drag.value;
  if (d?.kind !== "buf" || d.network !== net.id || d.name === buf.name) return clearDrag();
  e.preventDefault();
  // Reorder net.buffers directly (status buffer is neither draggable nor a drop
  // target, so it stays put). Send the channel order back, status excluded.
  const from = net.buffers.findIndex((b) => b.name === d.name);
  const to = net.buffers.findIndex((b) => b.name === buf.name);
  if (from < 0 || to < 0) return clearDrag();
  net.buffers = moveRelative(net.buffers, from, to, dropPos(e));
  connection.reorderBuffers(net.id, channelBuffers(net).map((b) => b.name));
  clearDrag();
}
</script>

<template>
  <nav class="sidebar" :class="{ open: ui.sidebarOpen }">
    <div class="brand">stugan</div>

    <div v-for="net in store.networks" :key="net.id" class="network">
      <div
        class="network-name"
        :class="{
          active: isActive(net.id, STATUS),
          'drop-before': hintFor(netKey(net)) === 'before',
          'drop-after': hintFor(netKey(net)) === 'after',
        }"
        draggable="true"
        @click="selectBuffer(net.id, STATUS)"
        @dragstart="onNetDragStart(net, $event)"
        @dragover="onNetDragOver(net, $event)"
        @drop="onNetDrop(net, $event)"
        @dragend="clearDrag"
        title="server status — click to open; drag to reorder; ⚙ for network settings"
      >
        <span class="net-label">{{ net.name }} <span class="nick">({{ net.nick }})</span></span>
        <span class="net-right">
          <span
            v-if="statusBuf(net) && statusBuf(net)!.unread > 0 && !isActive(net.id, STATUS)"
            class="badge"
            :class="{ highlight: statusBuf(net)!.highlight > 0 }"
            >{{ statusBuf(net)!.unread }}</span
          >
          <span class="net-gear" @click.stop="settingsFor = net.id" title="network settings">⚙</span>
        </span>
      </div>
      <ul class="buffers">
        <li
          v-for="buf in channelBuffers(net)"
          :key="buf.name"
          :class="{
            active: isActive(net.id, buf.name),
            [buf.kind]: true,
            muted: connection.isMuted(bufKey(net.id, buf.name)),
            'drop-before': hintFor(bufDropKey(net, buf)) === 'before',
            'drop-after': hintFor(bufDropKey(net, buf)) === 'after',
          }"
          draggable="true"
          @click="selectBuffer(net.id, buf.name)"
          @contextmenu="ctx.onContext({ network: net.id, buffer: buf.name, kind: buf.kind }, $event)"
          @dragstart="onBufDragStart(net, buf, $event)"
          @dragover="onBufDragOver(net, buf, $event)"
          @drop="onBufDrop(net, buf, $event)"
          @dragend="clearDrag"
          @touchstart.passive="ctx.onTouchStart({ network: net.id, buffer: buf.name, kind: buf.kind }, $event)"
          @touchmove.passive="ctx.onTouchMove($event)"
          @touchend="ctx.cancelLp"
          @touchcancel="ctx.cancelLp"
          title="right-click (or long-press) for buffer options; drag to reorder"
        >
          <span v-if="isEncrypted(buf)" class="lock-icon" :title="`encrypted (${buf.state.encrypted})`">🔒</span>
          <span class="buf-name">{{ buf.name }}</span>
          <span v-if="connection.isMuted(bufKey(net.id, buf.name))" class="mute-icon">🔇</span>
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
        {{ connection.isMuted(bufKey(ctx.state.value.payload.network, ctx.state.value.payload.buffer)) ? "Unmute" : "Mute" }}
      </button>
      <button class="ctx-item" type="button" @click="keyFromMenu">
        Set encryption key…
      </button>
      <button
        v-if="ctx.state.value.payload.kind === 'channel'"
        class="ctx-item"
        type="button"
        @click="leaveFromMenu"
      >
        Leave channel
      </button>
      <button
        v-if="ctx.state.value.payload.kind === 'query'"
        class="ctx-item"
        type="button"
        @click="closeFromMenu"
      >
        Close query
      </button>
    </div>

    <span class="conn-pill sidebar-conn" :class="store.status" :title="statusLabel">
      <span class="conn-dot" aria-hidden="true" />
      <span class="conn-label">{{ statusLabel }}</span>
    </span>
  </nav>
</template>
