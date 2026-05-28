<script setup lang="ts">
import { ref } from "vue";
import { connection } from "../connection";

// Dialog for setting / removing a FiSH (Blowfish) encryption key on a
// channel or query. The actual key handling lives in the server-side
// fish.lua plugin; we just send the slash-commands it claims:
//
//   /setkey [target] <key>       → CBC (default)
//   /setkey-ecb [target] <key>   → ECB (legacy, kept for old peers)
//   /delkey [target]
//
// We don't (yet) know whether a key is already set — the plugin's KV is
// server-side and there's no proto frame to query it. Surfacing current
// state would mean adding a "fish:get" round-trip; for v1 we just present
// Set/Remove and let the user choose.

const props = defineProps<{ network: string; buffer: string }>();
const emit = defineEmits<{ close: [] }>();

const key = ref("");
const mode = ref<"cbc" | "ecb">("cbc");
const show = ref(false); // toggles password-style masking

function setKey() {
  const k = key.value.trim();
  if (!k) return;
  const cmd = mode.value === "ecb" ? "/setkey-ecb" : "/setkey";
  // Address the buffer by name explicitly so the command behaves the same
  // whether the dialog was opened from the active buffer or another one.
  connection.send(props.network, props.buffer, `${cmd} ${props.buffer} ${k}`);
  emit("close");
}

function removeKey() {
  connection.send(props.network, props.buffer, `/delkey ${props.buffer}`);
  emit("close");
}
</script>

<template>
  <div class="settings-overlay" @click.self="emit('close')">
    <form class="settings" @submit.prevent="setKey">
      <h2>Encryption key</h2>
      <p class="hint">
        FiSH Blowfish encryption for <code>{{ buffer }}</code
        >. Both ends of the conversation need the same key — share it out of band.
      </p>
      <label class="row">
        <span>Mode</span>
        <select v-model="mode">
          <option value="cbc">CBC (recommended)</option>
          <option value="ecb">ECB (legacy)</option>
        </select>
      </label>
      <label class="row">
        <span>Key</span>
        <input
          v-model="key"
          :type="show ? 'text' : 'password'"
          placeholder="shared secret"
          autofocus
        />
      </label>
      <label class="row theme-row">
        <span>Show key</span>
        <input v-model="show" type="checkbox" />
      </label>
      <p class="hint">
        Setting a new key replaces any existing one for this buffer. The
        <code>fish.lua</code> plugin must be installed in
        <code>$STUGAN_HOME/scripts/</code> for keys to take effect.
      </p>
      <div class="row">
        <button type="button" class="danger" @click="removeKey">Remove key</button>
        <span class="spacer"></span>
        <button type="button" @click="emit('close')">Cancel</button>
        <button type="submit" :disabled="!key.trim()">Set key</button>
      </div>
    </form>
  </div>
</template>
