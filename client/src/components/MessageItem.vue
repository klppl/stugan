<script setup lang="ts">
import { computed, onMounted } from "vue";
import type { MessageDTO } from "../proto/events";
import { segments, extractURLs, isImage, isVideo, proxied } from "../links";
import { getPreview, fetchPreview, type Preview } from "../previews";

const props = defineProps<{ msg: MessageDTO; showBuffer?: boolean }>();

function time(iso: string): string {
  if (!iso) return "";
  return new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
}

const segs = computed(() => segments(props.msg.text));
const urls = computed(() => extractURLs(props.msg.text));
const media = computed(() => urls.value.filter((u) => isImage(u) || isVideo(u)).slice(0, 4));
const links = computed(() => urls.value.filter((u) => !isImage(u) && !isVideo(u)).slice(0, 2));

onMounted(() => {
  if (props.msg.kind === "privmsg" || props.msg.kind === "action") {
    for (const u of links.value) fetchPreview(u);
  }
});

function preview(u: string): Preview | null {
  const e = getPreview(u);
  return e && e !== "loading" && e !== "error" ? (e as Preview) : null;
}
</script>

<template>
  <div class="message" :class="[msg.kind, { highlight: msg.highlight, self: msg.self }]">
    <span class="ts">{{ time(msg.time) }}</span>
    <span v-if="showBuffer" class="loc">{{ msg.buffer }}</span>

    <template v-if="msg.kind === 'system'">
      <span class="sys">— {{ msg.text }}</span>
    </template>
    <template v-else-if="msg.kind === 'action'">
      <span class="body">* {{ msg.from }} <span class="seg">{{ msg.text }}</span></span>
    </template>
    <template v-else>
      <span class="from" :class="{ self: msg.self }">{{ msg.from }}</span>
      <span class="body">
        <template v-for="(s, i) in segs" :key="i">
          <a v-if="s.type === 'link'" :href="s.value" target="_blank" rel="noopener noreferrer">{{ s.value }}</a>
          <span v-else class="seg">{{ s.value }}</span>
        </template>
      </span>
    </template>

    <!-- inline media -->
    <div v-if="media.length" class="embeds">
      <template v-for="u in media" :key="u">
        <video v-if="isVideo(u)" :src="proxied(u)" controls preload="metadata" class="embed-media" />
        <a v-else :href="u" target="_blank" rel="noopener noreferrer">
          <img :src="proxied(u)" loading="lazy" class="embed-media" alt="" />
        </a>
      </template>
    </div>

    <!-- link previews -->
    <template v-for="u in links" :key="'p' + u">
      <a v-if="preview(u)" :href="u" target="_blank" rel="noopener noreferrer" class="preview-card">
        <img v-if="preview(u)!.image" :src="proxied(preview(u)!.image)" class="preview-img" loading="lazy" alt="" />
        <span class="preview-text">
          <span class="preview-title">{{ preview(u)!.title }}</span>
          <span class="preview-desc">{{ preview(u)!.description }}</span>
        </span>
      </a>
    </template>
  </div>
</template>
