// Composer drafts are session-local and keyed by the same folded
// network/buffer identity as the rest of the connection store. Keeping them
// in a module lets them survive ChatInput unmounts in search/mentions views.
const drafts = new Map<string, string>();

export function readDraft(key: string): string {
  return key ? drafts.get(key) ?? "" : "";
}

export function writeDraft(key: string, text: string) {
  if (!key) return;
  if (text) drafts.set(key, text);
  else drafts.delete(key);
}
