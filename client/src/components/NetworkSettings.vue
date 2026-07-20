<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from "vue";
import { connection } from "../connection";
import type { NetConfig } from "../proto/events";

const props = defineProps<{ network: string }>();
const emit = defineEmits<{ close: [] }>();

const netState = computed(
  () => connection.store.networks.find((n) => n.id === props.network)?.state ?? "disconnected",
);
const connected = computed(() => netState.value !== "disconnected");

// caps are the IRCv3 capabilities the live connection negotiated (populated
// on snapshots; empty while disconnected). features maps the ones the UI
// cares about to a human label so the user can see why an affordance like
// reactions is or isn't available. See connection.hasNetCap.
const caps = computed(() => connection.store.networks.find((n) => n.id === props.network)?.caps ?? []);
const features = computed(() => {
  const has = (...cs: string[]) => cs.some((c) => caps.value.includes(c));
  return [
    { label: "Reactions & typing", cap: "message-tags", on: has("message-tags") },
    { label: "Delete messages", cap: "draft/message-redaction", on: has("draft/message-redaction") },
    { label: "History sync", cap: "draft/chathistory", on: has("draft/chathistory", "chathistory") },
  ];
});

function toggleConnected() {
  connection.setConnected(props.network, !connected.value);
}

const loaded = ref(false);
const showAdvanced = ref(false);
const form = reactive({
  host: "",
  port: 6697,
  tls: true,
  insecure: false,
  fallbacks: "",
  nick: "",
  user: "",
  realname: "",
  saslUser: "",
  saslPass: "",
  serverPass: "",
  perform: "",
  saslExternal: false,
  certPem: "",
  channels: "",
});

function fill(cfg: NetConfig) {
  const [host, port] = splitAddr(cfg.addr);
  form.host = host;
  form.port = port;
  form.tls = cfg.tls;
  form.insecure = cfg.insecure;
  form.fallbacks = (cfg.fallbacks ?? []).join(", ");
  form.nick = cfg.nick;
  form.user = cfg.user;
  form.realname = cfg.realname;
  form.saslUser = cfg.sasl_user;
  form.saslPass = cfg.sasl_pass;
  form.serverPass = cfg.server_pass;
  form.perform = (cfg.perform ?? []).join("\n");
  form.saslExternal = cfg.sasl_external;
  form.certPem = cfg.cert_pem;
  form.channels = (cfg.channels ?? []).join(", ");
  loaded.value = true;
}

function splitAddr(addr: string): [string, number] {
  const i = addr.lastIndexOf(":");
  if (i < 0) return [addr, 6697];
  return [addr.slice(0, i), Number(addr.slice(i + 1)) || 6697];
}

onMounted(() => {
  const cfg = connection.store.netConfigs[props.network];
  if (cfg) fill(cfg);
  connection.requestNetInfo(props.network); // refresh
});

watch(
  () => connection.store.netConfigs[props.network],
  (cfg) => {
    if (cfg) fill(cfg);
  },
);

function save() {
  connection.editNetwork({
    network: props.network,
    name: props.network,
    addr: `${form.host.trim()}:${form.port}`,
    tls: form.tls,
    insecure: form.tls && form.insecure,
    fallbacks: form.fallbacks
      .split(",")
      .map((a) => a.trim())
      .filter(Boolean),
    nick: form.nick.trim(),
    user: form.user.trim(),
    realname: form.realname.trim(),
    sasl_user: form.saslUser.trim(),
    sasl_pass: form.saslPass,
    server_pass: form.serverPass,
    sasl_external: form.saslExternal,
    cert_pem: form.certPem.trim(),
    perform: form.perform
      .split("\n")
      .map((c) => c.trim())
      .filter(Boolean),
    channels: form.channels
      .split(",")
      .map((c) => c.trim())
      .filter(Boolean),
  });
  emit("close");
}

function remove() {
  if (confirm(`Remove network "${props.network}"? This disconnects and forgets it.`)) {
    connection.removeNetwork(props.network);
    emit("close");
  }
}
</script>

<template>
  <div class="settings-overlay" @click.self="emit('close')">
    <form class="settings addnet" @submit.prevent="save">
      <h2>{{ network }} settings <span class="net-state">· {{ netState }}</span></h2>
      <p v-if="!loaded" class="hint">Loading…</p>
      <template v-else>
        <label class="row"><span>Host</span><input v-model="form.host" /></label>
        <label class="row"><span>Port</span><input v-model.number="form.port" type="number" /></label>
        <label class="row"><span>TLS</span><input v-model="form.tls" type="checkbox" /></label>
        <label class="row"><span>Nick</span><input v-model="form.nick" /></label>
        <label class="row"><span>User</span><input v-model="form.user" placeholder="(optional)" /></label>
        <label class="row"><span>Realname</span><input v-model="form.realname" placeholder="(optional)" /></label>
        <label class="row"><span>Channels</span><input v-model="form.channels" placeholder="#one, #two" /></label>
        <label class="row"><span>SASL user</span><input v-model="form.saslUser" placeholder="(optional)" /></label>
        <label class="row"><span>SASL pass</span><input v-model="form.saslPass" type="password" placeholder="(unchanged)" /></label>

        <div class="row">
          <span></span>
          <button type="button" class="link" @click="showAdvanced = !showAdvanced">
            {{ showAdvanced ? "Hide advanced" : "Advanced…" }}
          </button>
        </div>
        <template v-if="showAdvanced">
          <label class="row"><span>Fallback servers</span><input v-model="form.fallbacks" placeholder="host:port, host2:port (tried if the primary fails)" /></label>
          <label class="row"><span>Server pass</span><input v-model="form.serverPass" type="password" placeholder="bouncer / server password" /></label>
          <label class="row">
            <span>Perform</span>
            <textarea v-model="form.perform" rows="3" spellcheck="false" placeholder="/msg NickServ IDENTIFY hunter2&#10;/join #private secretkey" />
          </label>
          <label v-if="form.tls" class="row"><span>Allow self-signed</span><input v-model="form.insecure" type="checkbox" /></label>
          <label class="row"><span>SASL EXTERNAL</span><input v-model="form.saslExternal" type="checkbox" /></label>
          <label class="row">
            <span>Client cert</span>
            <textarea v-model="form.certPem" rows="4" spellcheck="false" placeholder="PEM cert + key for CertFP" />
          </label>
        </template>
        <p class="hint">
          Nick and channel changes apply live. Server, TLS, user/realname,
          SASL, server-password, or client-certificate changes reconnect the
          network. Perform runs on every reconnect, one second apart and before
          channel auto-join. Variables: $me/$nick, $network, $server, $user,
          and $realname.
        </p>

        <div class="caps-section">
          <h3>Server features (IRCv3)</h3>
          <p v-if="caps.length === 0" class="hint">
            {{ connected ? "This server negotiated no IRCv3 capabilities." : "Connect to see what this server supports." }}
          </p>
          <template v-else>
            <ul class="feature-list">
              <li v-for="f in features" :key="f.cap" :class="{ off: !f.on }">
                <span class="feature-mark">{{ f.on ? "✓" : "—" }}</span>
                <span class="feature-label">{{ f.label }}</span>
                <code class="feature-cap">{{ f.cap }}</code>
              </li>
            </ul>
            <details class="caps-raw">
              <summary>All negotiated caps ({{ caps.length }})</summary>
              <div class="cap-chips">
                <span v-for="c in caps" :key="c" class="cap-chip">{{ c }}</span>
              </div>
            </details>
          </template>
        </div>

        <div class="row">
          <button type="button" class="danger" @click="remove">Remove network</button>
          <button type="button" @click="toggleConnected">{{ connected ? "Disconnect" : "Connect" }}</button>
          <span class="spacer" />
          <button type="button" @click="emit('close')">Cancel</button>
          <button type="submit">Save</button>
        </div>
      </template>
    </form>
  </div>
</template>

<style scoped>
.caps-section {
  margin-top: 14px;
}
.caps-section h3 {
  margin: 0 0 6px;
  font-size: 0.9em;
  font-weight: 600;
  color: var(--fg-dim);
}
.feature-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.feature-list li {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 0.9em;
}
.feature-list li.off {
  color: var(--fg-dim);
}
.feature-mark {
  width: 1em;
  text-align: center;
  color: var(--self);
}
.feature-list li.off .feature-mark {
  color: var(--fg-dim);
}
.feature-cap {
  margin-left: auto;
  font-size: 0.82em;
  color: var(--fg-dim);
}
.caps-raw {
  margin-top: 8px;
  font-size: 0.85em;
}
.caps-raw summary {
  cursor: pointer;
  color: var(--fg-dim);
}
.cap-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-top: 6px;
}
.cap-chip {
  font-size: 0.8em;
  padding: 1px 6px;
  border-radius: 4px;
  background: var(--bg-alt);
  color: var(--fg-dim);
}
</style>
