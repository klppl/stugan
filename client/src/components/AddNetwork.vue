<script setup lang="ts">
import { ref } from "vue";
import { connection } from "../connection";

const emit = defineEmits<{ close: [] }>();

const name = ref("");
const host = ref("");
const port = ref(6697);
const tls = ref(true);
const nick = ref("");
const channels = ref("");
const saslUser = ref("");
const saslPass = ref("");
const error = ref("");

function submit() {
  if (!name.value.trim() || !host.value.trim() || !nick.value.trim()) {
    error.value = "Name, host and nick are required";
    return;
  }
  connection.addNetwork({
    name: name.value.trim(),
    addr: `${host.value.trim()}:${port.value}`,
    tls: tls.value,
    nick: nick.value.trim(),
    sasl_user: saslUser.value.trim() || undefined,
    sasl_pass: saslPass.value || undefined,
    channels: channels.value
      .split(",")
      .map((c) => c.trim())
      .filter(Boolean),
  });
  emit("close");
}
</script>

<template>
  <div class="settings-overlay" @click.self="emit('close')">
    <form class="settings addnet" @submit.prevent="submit">
      <h2>Add network</h2>
      <label class="row"><span>Name</span><input v-model="name" placeholder="libera" /></label>
      <label class="row"><span>Host</span><input v-model="host" placeholder="irc.libera.chat" /></label>
      <label class="row"><span>Port</span><input v-model.number="port" type="number" /></label>
      <label class="row"><span>TLS</span><input v-model="tls" type="checkbox" /></label>
      <label class="row"><span>Nick</span><input v-model="nick" placeholder="yournick" /></label>
      <label class="row"><span>Channels</span><input v-model="channels" placeholder="#one, #two" /></label>
      <label class="row"><span>SASL user</span><input v-model="saslUser" placeholder="(optional)" /></label>
      <label class="row"><span>SASL pass</span><input v-model="saslPass" type="password" placeholder="(optional)" /></label>
      <p v-if="error" class="login-error">{{ error }}</p>
      <div class="row">
        <button type="button" @click="emit('close')">Cancel</button>
        <button type="submit">Add &amp; connect</button>
      </div>
    </form>
  </div>
</template>
