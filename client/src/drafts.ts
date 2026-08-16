import { reactive } from "vue";
import type { DraftDTO } from "./proto/events";

// Composer drafts are persisted on the server per user and synced across tabs,
// keyed by the folded network/buffer unit-separated identity.
export const drafts = reactive<Record<string, string>>({});

const draftTimers: Record<string, number> = {};

export function readDraft(key: string): string {
  return key ? drafts[key] ?? "" : "";
}

export function writeDraft(key: string, text: string, syncRemote = true) {
  if (!key) return;
  if (text) {
    drafts[key] = text;
  } else {
    delete drafts[key];
  }

  if (syncRemote) {
    const parts = key.split("\x1f");
    if (parts.length === 2 && parts[0] && parts[1]) {
      const [network, buffer] = parts;
      if (draftTimers[key]) clearTimeout(draftTimers[key]);
      draftTimers[key] = window.setTimeout(() => {
        delete draftTimers[key];
        import("./connection")
          .then(({ connection }) => connection.sendDraft(network, buffer, text))
          .catch(() => {});
      }, 500);
    }
  }
}

export function setRemoteDraft(network: string, buffer: string, text: string) {
  if (!network || !buffer) return;
  const key = `${network}\x1f${buffer.toLowerCase()}`;
  if (draftTimers[key]) {
    clearTimeout(draftTimers[key]);
    delete draftTimers[key];
  }
  if (text) {
    drafts[key] = text;
  } else {
    delete drafts[key];
  }
}

export function loadDraftsPayload(list: DraftDTO[]) {
  if (!Array.isArray(list)) return;
  for (const d of list) {
    if (d.network && d.buffer) {
      setRemoteDraft(d.network, d.buffer, d.text);
    }
  }
}

