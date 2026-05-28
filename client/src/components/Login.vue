<script setup lang="ts">
import { ref } from "vue";
import { authState, login } from "../auth";

const username = ref("");
const password = ref("");
const busy = ref(false);
// Honeypot fields — see MagicWord.vue for the rationale.
const hp_email = ref("");
const hp_website = ref("");

async function submit() {
  if (busy.value) return;
  busy.value = true;
  await login(username.value, password.value, { email: hp_email.value, website: hp_website.value });
  busy.value = false;
  password.value = "";
}
</script>

<template>
  <div class="login-screen">
    <form class="login" @submit.prevent="submit" autocomplete="off" novalidate>
      <div class="brand">stugan</div>
      <p class="login-sub">Sign in to continue.</p>

      <!-- Honeypot pair. CSS hides them from humans; bots' field-fillers
           are fooled by the common field names and will populate them. -->
      <div class="hp-field" aria-hidden="true">
        <label>Email<input v-model="hp_email" type="text" tabindex="-1" autocomplete="off" /></label>
        <label>Website<input v-model="hp_website" type="text" tabindex="-1" autocomplete="off" /></label>
      </div>

      <input v-model="username" placeholder="Username" autocomplete="username" autofocus />
      <input v-model="password" type="password" placeholder="Password" autocomplete="current-password" />
      <button type="submit" :disabled="busy">{{ busy ? "Signing in…" : "Sign in" }}</button>
      <p v-if="authState.error" class="login-error">{{ authState.error }}</p>
    </form>
  </div>
</template>
