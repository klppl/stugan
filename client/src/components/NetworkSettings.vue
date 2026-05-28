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

function toggleConnected() {
  connection.setConnected(props.network, !connected.value);
}

const loaded = ref(false);
const showAdvanced = ref(false);
const form = reactive({
  host: "",
  port: 6697,
  tls: true,
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
          <label class="row"><span>Server pass</span><input v-model="form.serverPass" type="password" placeholder="bouncer / server password" /></label>
          <label class="row">
            <span>Perform</span>
            <textarea v-model="form.perform" rows="3" spellcheck="false" placeholder="/msg NickServ IDENTIFY hunter2&#10;/join #private secretkey" />
          </label>
          <label class="row"><span>SASL EXTERNAL</span><input v-model="form.saslExternal" type="checkbox" /></label>
          <label class="row">
            <span>Client cert</span>
            <textarea v-model="form.certPem" rows="4" spellcheck="false" placeholder="PEM cert + key for CertFP" />
          </label>
        </template>
        <p class="hint">
          Nick and channel changes apply live. Server, TLS, user/realname,
          SASL, server-password, or client-certificate changes reconnect the
          network. Perform runs on every reconnect.
        </p>
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
