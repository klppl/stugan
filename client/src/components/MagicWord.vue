<script setup lang="ts">
import { ref } from "vue";
import { authState, submitMagicWord } from "../auth";

const word = ref("");
const busy = ref(false);
// Honeypot fields: these inputs are rendered in the DOM but hidden from
// real users. A form-filling bot that auto-completes by field name will
// populate them; the server reads them as a tell and rejects the
// submission. The values are never used otherwise.
const hp_email = ref("");
const hp_website = ref("");

async function submit() {
  if (!word.value || busy.value) return;
  busy.value = true;
  await submitMagicWord(word.value, { email: hp_email.value, website: hp_website.value });
  busy.value = false;
  word.value = "";
}
</script>

<template>
  <div class="login-screen">
    <form class="login" @submit.prevent="submit" autocomplete="off" novalidate>
      <div class="brand">stugan</div>
      <p class="login-sub">This server requires a password to continue.</p>

      <!-- Honeypot pair. Real users never see or focus these. -->
      <div class="hp-field" aria-hidden="true">
        <label>Email<input v-model="hp_email" type="text" tabindex="-1" autocomplete="off" /></label>
        <label>Website<input v-model="hp_website" type="text" tabindex="-1" autocomplete="off" /></label>
      </div>

      <input
        v-model="word"
        type="password"
        placeholder="Password"
        autocomplete="current-password"
        autofocus
        :disabled="busy"
      />
      <button type="submit" :disabled="busy || !word">
        {{ busy ? "Checking…" : "Continue" }}
      </button>
      <p v-if="authState.magicError" class="login-error">{{ authState.magicError }}</p>
    </form>
  </div>
</template>
