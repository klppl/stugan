<script setup lang="ts">
import { computed, inject, onMounted, ref } from "vue";
import type { MessageDTO } from "../proto/events";
import { segments, extractURLs, isImage, isVideo, proxied, stripFormatting } from "../links";
import { getPreview, fetchPreview, type Preview } from "../previews";
import { settings } from "../settings";
import { nickColor } from "../nickColor";
import { connection } from "../connection";

const props = defineProps<{ msg: MessageDTO; showBuffer?: boolean; showDate?: boolean }>();

// A small fixed palette for one-click reactions; the chip row also lets you
// toggle any reaction already present.
const QUICK_REACTS = ["👍", "❤️", "😂", "🎉", "😮", "😢"];
const showReactPicker = ref(false);

// Reactions for this message (keyed globally by msgid). Returns one entry per
// distinct emoji with its count and whether we reacted, sorted for stability.
const reactions = computed(() => {
  const byEmoji = props.msg.id ? connection.store.reactions[props.msg.id] : undefined;
  if (!byEmoji) return [];
  const me = connection.nickOn(props.msg.network).toLowerCase();
  return Object.entries(byEmoji)
    .map(([emoji, nicks]) => ({
      emoji,
      count: nicks.length,
      mine: nicks.some((n) => n.toLowerCase() === me),
      who: nicks.join(", "),
    }))
    .sort((a, b) => a.emoji.localeCompare(b.emoji));
});

// Affordances are only meaningful on real chat lines that carry a msgid, in
// the live buffer (not search/mention previews), on networks that negotiated
// the relevant cap.
const interactive = computed(
  () => !props.showBuffer && !!props.msg.id && (props.msg.kind === "privmsg" || props.msg.kind === "notice" || props.msg.kind === "action"),
);
const canReact = computed(
  () => settings.reactions && interactive.value && connection.hasNetCap(props.msg.network, "message-tags"),
);
const canRedact = computed(
  () => interactive.value && props.msg.self && connection.hasNetCap(props.msg.network, "draft/message-redaction"),
);

function toggleReact(emoji: string) {
  showReactPicker.value = false;
  connection.react(props.msg.network, props.msg.buffer, props.msg.id, emoji);
}
function doRedact() {
  connection.redact(props.msg.network, props.msg.buffer, props.msg.id);
}

function time(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  const clock = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
  if (!props.showDate) return clock;
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${clock}`;
}

// Membership churn (join/part/quit/nick) and true system notices both render
// as a dim "— text" line; only privmsg/notice/action carry a sender + body.
const EVENT_KINDS = ["join", "part", "quit", "nick"];
const isSystemLine = computed(
  () => props.msg.kind === "system" || EVENT_KINDS.includes(props.msg.kind),
);

// Our own nick keeps the dedicated "self" color; everyone else is hashed.
function fromColor(nick: string): string {
  if (props.msg.self || !settings.coloredNicks) return "";
  return nickColor(nick);
}

const segs = computed(() => segments(props.msg.text));
// Raw-text render paths (system lines, /me) strip mIRC formatting codes the
// same way segments() does for the normal body.
const cleanText = computed(() => stripFormatting(props.msg.text));
const urls = computed(() => extractURLs(props.msg.text));
const media = computed(() => urls.value.filter((u) => isImage(u) || isVideo(u)).slice(0, 4));
const links = computed(() => urls.value.filter((u) => !isImage(u) && !isVideo(u)).slice(0, 2));
const previewURLs = computed(() => [...media.value, ...links.value]);
// The setting supplies the initial state for each link. A copied Set on each
// change keeps Vue's reactivity explicit and lets previews in the same message
// be expanded independently.
const expandedPreviews = ref(new Set(settings.expandLinkPreviews ? previewURLs.value : []));

function previewExpanded(u: string): boolean {
  return expandedPreviews.value.has(u);
}

function togglePreview(u: string) {
  const next = new Set(expandedPreviews.value);
  if (next.has(u)) next.delete(u);
  else next.add(u);
  expandedPreviews.value = next;
}

function isPreviewLink(u: string): boolean {
  return previewURLs.value.includes(u);
}

onMounted(() => {
  if (props.msg.kind === "privmsg" || props.msg.kind === "action") {
    for (const u of links.value) fetchPreview(u);
  }
});

function preview(u: string): Preview | null {
  const e = getPreview(u);
  return e && e !== "loading" && e !== "error" ? (e as Preview) : null;
}

// The bare host (sans www.) shown as a small label above the title, giving
// the card some provenance the way Slack/Discord unfurls do.
function host(u: string): string {
  try {
    return new URL(u).hostname.replace(/^www\./, "");
  } catch {
    return "";
  }
}

// nickCtx is provided by ChatView; it forwards right-click and long-press
// on a sender nick into the same buffer-list context menu (WHOIS, ignore,
// DM, mode shortcuts, kick, …). Falls back to a no-op object so this
// component still renders if used in a context without the provider
// (e.g. unit tests).
interface NickCtx {
  onContext: (nick: string, ev: MouseEvent) => void;
  onTouchStart: (nick: string, ev: TouchEvent) => void;
  onTouchMove: (ev: TouchEvent) => void;
  cancelLp: () => void;
}
const nickCtx = inject<NickCtx>("nickCtx", {
  onContext: () => {},
  onTouchStart: () => {},
  onTouchMove: () => {},
  cancelLp: () => {},
});
</script>

<template>
  <div
    class="message"
    :class="[msg.kind, { highlight: msg.highlight, self: msg.self }]"
    :data-msgid="msg.id || undefined"
  >
    <span class="ts">{{ time(msg.time) }}</span>
    <span v-if="showBuffer" class="loc">{{ msg.buffer }}</span>

    <template v-if="isSystemLine">
      <span class="sys" :class="msg.kind"><span class="sys-pre">—</span> {{ cleanText }}</span>
    </template>
    <template v-else-if="msg.kind === 'action'">
      <span class="body">*
        <span
          class="from long-press-target"
          :class="{ self: msg.self }"
          :style="{ color: fromColor(msg.from) }"
          title="right-click (or long-press) for nick options"
          @contextmenu="nickCtx.onContext(msg.from, $event)"
          @touchstart.passive="nickCtx.onTouchStart(msg.from, $event)"
          @touchmove.passive="nickCtx.onTouchMove($event)"
          @touchend="nickCtx.cancelLp"
          @touchcancel="nickCtx.cancelLp"
          @selectstart.prevent
        >{{ msg.from }}</span>{{ " " }}
        <template v-for="(s, i) in segs" :key="i">
          <template v-if="s.type === 'link'">
            <a :href="s.value" target="_blank" rel="noopener noreferrer">{{ s.value }}</a>
            <button
              v-if="isPreviewLink(s.value)"
              type="button"
              class="link-preview-toggle"
              :class="{ expanded: previewExpanded(s.value) }"
              :aria-label="previewExpanded(s.value) ? 'Hide link preview' : 'Show link preview'"
              :aria-expanded="previewExpanded(s.value)"
              :title="previewExpanded(s.value) ? 'Hide preview' : 'Show preview'"
              @click="togglePreview(s.value)"
            >
              <svg aria-hidden="true" viewBox="0 0 16 16" focusable="false">
                <path d="m3 6 5 5 5-5" />
              </svg>
            </button>
          </template>
          <span v-else class="seg">{{ s.value }}</span>
        </template></span>
    </template>
    <template v-else>
      <span
        class="from long-press-target"
        :class="{ self: msg.self }"
        :style="{ color: fromColor(msg.from) }"
        title="right-click (or long-press) for nick options"
        @contextmenu="nickCtx.onContext(msg.from, $event)"
        @touchstart.passive="nickCtx.onTouchStart(msg.from, $event)"
        @touchmove.passive="nickCtx.onTouchMove($event)"
        @touchend="nickCtx.cancelLp"
        @touchcancel="nickCtx.cancelLp"
        @selectstart.prevent
      >{{ msg.from }}</span>
      <span class="body">
        <template v-for="(s, i) in segs" :key="i">
          <template v-if="s.type === 'link'">
            <a :href="s.value" target="_blank" rel="noopener noreferrer">{{ s.value }}</a>
            <button
              v-if="isPreviewLink(s.value)"
              type="button"
              class="link-preview-toggle"
              :class="{ expanded: previewExpanded(s.value) }"
              :aria-label="previewExpanded(s.value) ? 'Hide link preview' : 'Show link preview'"
              :aria-expanded="previewExpanded(s.value)"
              :title="previewExpanded(s.value) ? 'Hide preview' : 'Show preview'"
              @click="togglePreview(s.value)"
            >
              <svg aria-hidden="true" viewBox="0 0 16 16" focusable="false">
                <path d="m3 6 5 5 5-5" />
              </svg>
            </button>
          </template>
          <span v-else class="seg">{{ s.value }}</span>
        </template>
      </span>
    </template>

    <!-- inline media -->
    <div v-if="media.some((u) => previewExpanded(u))" class="embeds">
      <template v-for="u in media" :key="u">
        <video v-if="isVideo(u) && previewExpanded(u)" :src="proxied(u)" controls preload="metadata" class="embed-media" />
        <a v-else-if="previewExpanded(u)" :href="u" target="_blank" rel="noopener noreferrer">
          <img :src="proxied(u)" loading="lazy" class="embed-media" alt="" />
        </a>
      </template>
    </div>

    <!-- Expanded cards sit below the message; their arrow controls live beside
         the corresponding links above. -->
    <div v-if="links.some((u) => preview(u) && previewExpanded(u))" class="previews">
      <template v-for="u in links" :key="'p' + u">
        <a v-if="preview(u) && previewExpanded(u)" :href="u" target="_blank" rel="noopener noreferrer" class="preview-card">
          <img v-if="preview(u)!.image" :src="proxied(preview(u)!.image)" class="preview-img" loading="lazy" alt="" />
          <span class="preview-text">
            <span v-if="host(u)" class="preview-host">{{ host(u) }}</span>
            <span class="preview-title">{{ preview(u)!.title }}</span>
            <span v-if="preview(u)!.description" class="preview-desc">{{ preview(u)!.description }}</span>
          </span>
        </a>
      </template>
    </div>

    <!-- reactions -->
    <div v-if="settings.reactions && reactions.length" class="reactions">
      <button
        v-for="r in reactions"
        :key="r.emoji"
        type="button"
        class="reaction"
        :class="{ mine: r.mine }"
        :title="r.who"
        :disabled="!canReact"
        @click="toggleReact(r.emoji)"
      >{{ r.emoji }} {{ r.count }}</button>
    </div>

    <!-- hover toolbar: react / delete -->
    <span v-if="canReact || canRedact" class="msg-actions">
      <button v-if="canReact" type="button" class="msg-act" title="Add reaction" @click="showReactPicker = !showReactPicker">☺+</button>
      <button v-if="canRedact" type="button" class="msg-act danger" title="Delete message" @click="doRedact">✕</button>
      <span v-if="showReactPicker" class="react-picker">
        <button v-for="e in QUICK_REACTS" :key="e" type="button" @click="toggleReact(e)">{{ e }}</button>
      </span>
    </span>
  </div>
</template>
