// URL detection and rendering helpers.

const URL_RE = /(https?:\/\/[^\s<>"']+)/g;

// mIRC formatting codes: color (0x03 with optional fg[,bg] digits), hex color
// (0x04), and the toggles bold/italic/underline/strikethrough/monospace/
// reverse/reset. We don't render them (yet) — strip them so a colored message
// shows its text instead of stray digits and invisible control characters
// (which otherwise also pollute copy/paste and notification bodies).
// eslint-disable-next-line no-control-regex
const IRC_FORMAT_RE = /\x03\d{0,2}(?:,\d{1,2})?|\x04(?:[0-9a-fA-F]{6}(?:,[0-9a-fA-F]{6})?)?|[\x02\x0F\x11\x16\x1D\x1E\x1F]/g;

export function stripFormatting(text: string): string {
  return text.replace(IRC_FORMAT_RE, "");
}

export interface Segment {
  type: "text" | "link";
  value: string;
}

// segments splits a message into plain-text and link runs for safe rendering
// (the template binds each as text, never as HTML). Formatting codes are
// stripped first so they can't corrupt the display or split a URL.
export function segments(text: string): Segment[] {
  text = stripFormatting(text);
  const out: Segment[] = [];
  let last = 0;
  for (const m of text.matchAll(URL_RE)) {
    const i = m.index ?? 0;
    if (i > last) out.push({ type: "text", value: text.slice(last, i) });
    out.push({ type: "link", value: m[0] });
    last = i + m[0].length;
  }
  if (last < text.length) out.push({ type: "text", value: text.slice(last) });
  return out;
}

export function extractURLs(text: string): string[] {
  return Array.from(stripFormatting(text).matchAll(URL_RE), (m) => m[0]);
}

export function isImage(url: string): boolean {
  return /\.(png|jpe?g|gif|webp|svg|avif|bmp)(\?|#|$)/i.test(url);
}

export function isVideo(url: string): boolean {
  return /\.(mp4|webm|mov|m4v)(\?|#|$)/i.test(url);
}

// proxied routes a remote media URL through the daemon's image proxy.
export function proxied(url: string): string {
  return "/api/proxy?url=" + encodeURIComponent(url);
}
