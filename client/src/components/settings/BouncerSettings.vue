<script setup lang="ts">
import { computed, ref } from "vue";
import { connection } from "../../connection";

const bouncer = computed(() => connection.store.bouncer);
const networks = computed(() => connection.store.networks);

// The listener binds all interfaces unless the admin pinned a host, so the
// address the browser used to reach stugan is the best guess for clients.
const host = computed(() => bouncer.value?.host || window.location.hostname);
const port = computed(() => bouncer.value?.port ?? 6697);
const tls = computed(() => bouncer.value?.tls ?? false);
const authRequired = computed(() => bouncer.value?.auth_required ?? true);
const username = computed(() => bouncer.value?.username || "default");
const passwordHint = computed(() =>
  authRequired.value ? "Your stugan password" : "None needed (single-user mode)",
);

// login is the username a classic client uses to attach to one network.
function login(network: string): string {
  return `${username.value}/${network}`;
}

const selected = ref(networks.value[0]?.name ?? "");
const selectedNetwork = computed(
  () => networks.value.find((n) => n.name === selected.value)?.name ?? networks.value[0]?.name ?? "",
);

const weechat = computed(() => {
  const net = selectedNetwork.value;
  const name = `stugan-${net}`;
  const lines = [
    `/server add ${name} ${host.value}/${port.value}${tls.value ? " -tls" : ""}`,
    `/set irc.server.${name}.username "${login(net)}"`,
  ];
  if (authRequired.value) lines.push(`/set irc.server.${name}.password "<your stugan password>"`);
  if (bouncer.value?.fingerprint) {
    lines.push(`/set irc.server.${name}.tls_fingerprint "${bouncer.value.fingerprint.replace(/:/g, "").toLowerCase()}"`);
  }
  lines.push(`/connect ${name}`);
  return lines.join("\n");
});

const irssi = computed(() => {
  const net = selectedNetwork.value;
  const pass = authRequired.value ? ` ${login(net)}:<your stugan password>` : "";
  return `/server add${tls.value ? " -tls" : ""} -network stugan-${net} ${host.value} ${port.value}${pass}\n/connect stugan-${net}`;
});

function toggle(e: Event) {
  connection.setBouncerEnabled((e.target as HTMLInputElement).checked);
}

async function copy(text: string, what: string) {
  try {
    await navigator.clipboard.writeText(text);
    connection.showToast(`${what} copied to clipboard`, undefined, "info");
  } catch {
    connection.showToast("Couldn't copy to clipboard", undefined, "error");
  }
}
</script>

<template>
  <div class="settings-section">
    <div class="section-header">
      <h3 class="section-title">IRC Clients</h3>
      <p class="section-desc">
        Use stugan as a bouncer: connect HexChat, WeeChat, irssi, Halloy, Goguma or any other IRC client and share
        the same always-on connections, history and plugins as the web UI.
      </p>
    </div>

    <div v-if="bouncer && !bouncer.available" class="settings-card">
      <p class="card-subtitle">Not enabled on this server</p>
      <p class="setting-hint">
        The bouncer listener is off. Whoever runs this stugan instance can turn it on by adding an
        <code>[ircserver]</code> block to <code>config.toml</code> and restarting:
      </p>
      <pre class="bouncer-snippet code-font">[ircserver]
listen = "0.0.0.0:6697"
tls    = true   # self-signed certificate is generated if none is given</pre>
    </div>

    <template v-else-if="bouncer">
      <div class="settings-card">
        <label class="setting-row checkbox-row">
          <div class="setting-label">
            <span>Allow IRC clients to connect</span>
            <span class="setting-hint">
              Accept bouncer logins to this account. Turning this off disconnects any IRC client that is attached
              right now; your networks stay connected either way.
            </span>
          </div>
          <input :checked="bouncer.enabled" type="checkbox" class="toggle-switch" @change="toggle" />
        </label>
      </div>

      <template v-if="bouncer.enabled">
        <div class="settings-card">
          <p class="card-subtitle">Server details</p>
          <dl class="bouncer-grid">
            <dt>Server</dt>
            <dd>
              <code>{{ host }}</code>
              <button class="btn btn-sm" @click="copy(host, 'Server address')">Copy</button>
            </dd>
            <dt>Port</dt>
            <dd><code>{{ port }}</code></dd>
            <dt>TLS / SSL</dt>
            <dd>{{ tls ? "On" : "Off (plaintext)" }}</dd>
            <template v-if="bouncer.fingerprint">
              <dt>Certificate</dt>
              <dd class="bouncer-fp">
                <span class="setting-hint">
                  Self-signed. Pin this SHA-256 fingerprint in your client (or allow invalid certificates):
                </span>
                <code class="code-font">{{ bouncer.fingerprint }}</code>
                <button class="btn btn-sm" @click="copy(bouncer.fingerprint, 'Fingerprint')">Copy</button>
              </dd>
            </template>
            <dt>Password</dt>
            <dd>{{ passwordHint }}</dd>
          </dl>
        </div>

        <div class="settings-card">
          <p class="card-subtitle">Clients with multi-network support</p>
          <p class="setting-hint">
            Halloy, Goguma, gamja, senpai and other clients that speak <code>soju.im/bouncer-networks</code> need just
            one server entry. Log in with the username below and every network shows up on its own.
          </p>
          <dl class="bouncer-grid">
            <dt>Username</dt>
            <dd>
              <code>{{ username }}</code>
              <button class="btn btn-sm" @click="copy(username, 'Username')">Copy</button>
            </dd>
          </dl>
        </div>

        <div class="settings-card">
          <p class="card-subtitle">Other clients: one server entry per network</p>
          <p class="setting-hint">
            HexChat, WeeChat, irssi, mIRC, Textual and most others attach to a single network per connection. Add one
            server entry for each network below and put the network after a slash in the username. It works as both
            the plain username and the SASL PLAIN username.
            <template v-if="networks.length === 1">
              With only one network you can leave off the <code>/{{ networks[0].name }}</code> part.
            </template>
          </p>

          <p v-if="networks.length === 0" class="setting-hint">
            You have no networks yet. Add one in the sidebar first.
          </p>
          <table v-else class="bouncer-table">
            <thead>
              <tr>
                <th>Network</th>
                <th>Username</th>
                <th v-if="authRequired">Server password</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="n in networks" :key="n.id">
                <td>{{ n.name }}</td>
                <td>
                  <code>{{ login(n.name) }}</code>
                  <button class="btn btn-sm" @click="copy(login(n.name), 'Username')">Copy</button>
                </td>
                <td v-if="authRequired" class="setting-hint">your stugan password</td>
              </tr>
            </tbody>
          </table>
          <p v-if="authRequired && networks.length" class="setting-hint">
            If a client has no separate username field, put everything in the server password instead:
            <code>{{ login(networks[0].name) }}:&lt;password&gt;</code>.
          </p>
        </div>

        <div v-if="networks.length" class="settings-card">
          <div class="setting-row">
            <p class="card-subtitle">Ready-made commands</p>
            <select v-if="networks.length > 1" v-model="selected" class="setting-input" aria-label="Network">
              <option v-for="n in networks" :key="n.id" :value="n.name">{{ n.name }}</option>
            </select>
          </div>
          <div class="form-group">
            <span class="setting-hint">WeeChat</span>
            <pre class="bouncer-snippet code-font">{{ weechat }}</pre>
            <div class="row-action">
              <button class="btn btn-sm" @click="copy(weechat, 'WeeChat commands')">Copy</button>
            </div>
          </div>
          <div class="form-group">
            <span class="setting-hint">irssi</span>
            <pre class="bouncer-snippet code-font">{{ irssi }}</pre>
            <div class="row-action">
              <button class="btn btn-sm" @click="copy(irssi, 'irssi commands')">Copy</button>
            </div>
          </div>
          <p class="setting-hint">
            Typing <code>/quit</code> in an IRC client only detaches that client. stugan stays connected to the network.
          </p>
        </div>
      </template>
    </template>
  </div>
</template>

<style scoped>
.bouncer-grid {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 8px 16px;
  margin: 0;
  font-size: 13px;
  align-items: center;
}
.bouncer-grid dt {
  color: var(--fg-dim);
}
.bouncer-grid dd {
  margin: 0;
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.bouncer-fp code {
  word-break: break-all;
  font-size: 12px;
}
.bouncer-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.bouncer-table th {
  text-align: left;
  font-weight: 500;
  color: var(--fg-dim);
  padding: 4px 8px 6px 0;
  border-bottom: 1px solid var(--border);
}
.bouncer-table td {
  padding: 6px 8px 6px 0;
  border-bottom: 1px solid var(--border);
  vertical-align: middle;
}
.bouncer-table td code {
  margin-right: 8px;
  word-break: break-all;
}
.bouncer-snippet {
  margin: 0;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 10px 12px;
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-all;
  overflow-x: auto;
}
</style>
