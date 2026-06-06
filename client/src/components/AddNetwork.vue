<script setup lang="ts">
import { ref } from "vue";
import { connection } from "../connection";

const emit = defineEmits<{ close: [] }>();

const name = ref("");
const host = ref("");
const port = ref(6697);
const tls = ref(true);
const insecure = ref(false);
const fallbacks = ref("");
const nick = ref("");
const channels = ref("");
const saslUser = ref("");
const saslPass = ref("");
const serverPass = ref("");
const perform = ref("");
const saslExternal = ref(false);
const certPem = ref("");
const showAdvanced = ref(false);
const error = ref("");

// splitList parses a comma-separated input into trimmed entries, or undefined
// when empty (so the optional wire field is omitted rather than sent as []).
function splitList(s: string): string[] | undefined {
  const out = s
    .split(",")
    .map((x) => x.trim())
    .filter(Boolean);
  return out.length ? out : undefined;
}

function submit() {
  if (!name.value.trim() || !host.value.trim() || !nick.value.trim()) {
    error.value = "Name, host and nick are required";
    return;
  }
  connection.addNetwork({
    name: name.value.trim(),
    addr: `${host.value.trim()}:${port.value}`,
    tls: tls.value,
    insecure: (tls.value && insecure.value) || undefined,
    fallbacks: splitList(fallbacks.value),
    nick: nick.value.trim(),
    sasl_user: saslUser.value.trim() || undefined,
    sasl_pass: saslPass.value || undefined,
    server_pass: serverPass.value || undefined,
    sasl_external: saslExternal.value,
    cert_pem: certPem.value.trim() || undefined,
    perform: perform.value
      .split("\n")
      .map((c) => c.trim())
      .filter(Boolean),
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

      <div class="row">
        <span></span>
        <button type="button" class="link" @click="showAdvanced = !showAdvanced">
          {{ showAdvanced ? "Hide advanced" : "Advanced…" }}
        </button>
      </div>
      <template v-if="showAdvanced">
        <label class="row"><span>Fallback servers</span><input v-model="fallbacks" placeholder="host:port, host2:port (tried if the primary fails)" /></label>
        <label class="row"><span>Server pass</span><input v-model="serverPass" type="password" placeholder="bouncer / server password" /></label>
        <label class="row">
          <span>Perform</span>
          <textarea v-model="perform" rows="3" spellcheck="false" placeholder="/msg NickServ IDENTIFY hunter2&#10;/join #private secretkey" />
        </label>
        <label v-if="tls" class="row"><span>Allow self-signed</span><input v-model="insecure" type="checkbox" /></label>
        <label class="row"><span>SASL EXTERNAL</span><input v-model="saslExternal" type="checkbox" /></label>
        <label class="row">
          <span>Client cert</span>
          <textarea v-model="certPem" rows="4" spellcheck="false" placeholder="PEM cert + key for CertFP (-----BEGIN CERTIFICATE----- … -----END PRIVATE KEY-----)" />
        </label>
        <p class="hint">
          Perform runs one command per line after connecting (every reconnect).
          A client cert enables CertFP; tick SASL EXTERNAL to authenticate with it.
          Allow self-signed skips TLS certificate checks — only for trusted LAN
          servers, never the public internet.
        </p>
      </template>

      <p v-if="error" class="login-error">{{ error }}</p>
      <div class="row">
        <button type="button" @click="emit('close')">Cancel</button>
        <button type="submit">Add &amp; connect</button>
      </div>
    </form>
  </div>
</template>
