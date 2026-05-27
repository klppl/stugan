<script setup lang="ts">
import { onMounted, reactive, ref, watch } from "vue";
import { connection } from "../connection";
import type { NetConfig } from "../proto/events";

const props = defineProps<{ network: string }>();
const emit = defineEmits<{ close: [] }>();

const loaded = ref(false);
const form = reactive({
  host: "",
  port: 6697,
  tls: true,
  nick: "",
  user: "",
  realname: "",
  saslUser: "",
  saslPass: "",
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
      <h2>{{ network }} settings</h2>
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
        <p class="hint">
          Nick and channel changes apply live. Server, TLS, user/realname, or
          SASL changes reconnect the network.
        </p>
        <div class="row">
          <button type="button" class="danger" @click="remove">Remove network</button>
          <span class="spacer" />
          <button type="button" @click="emit('close')">Cancel</button>
          <button type="submit">Save</button>
        </div>
      </template>
    </form>
  </div>
</template>
