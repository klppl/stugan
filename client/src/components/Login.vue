<script setup lang="ts">
import { ref } from "vue";
import { authState, login } from "../auth";

const username = ref("");
const password = ref("");
const busy = ref(false);

async function submit() {
  busy.value = true;
  await login(username.value, password.value);
  busy.value = false;
  password.value = "";
}
</script>

<template>
  <div class="login-screen">
    <form class="login" @submit.prevent="submit">
      <div class="brand">stugan</div>
      <input v-model="username" placeholder="Username" autocomplete="username" autofocus />
      <input v-model="password" type="password" placeholder="Password" autocomplete="current-password" />
      <button type="submit" :disabled="busy">{{ busy ? "Signing in…" : "Sign in" }}</button>
      <p v-if="authState.error" class="login-error">{{ authState.error }}</p>
    </form>
  </div>
</template>
