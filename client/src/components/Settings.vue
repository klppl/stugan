<script setup lang="ts">
import SettingsView from "./settings/SettingsView.vue";
const emit = defineEmits<{ close: [] }>();
</script>

<template>
  <SettingsView @close="emit('close')" />
</template>
