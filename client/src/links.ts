// URL detection and rendering helpers.

const URL_RE = /(https?:\/\/[^\s<>"']+)/g;

export interface Segment {
  type: "text" | "link";
  value: string;
}

// segments splits a message into plain-text and link runs for safe rendering
// (the template binds each as text, never as HTML).
export function segments(text: string): Segment[] {
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
  return Array.from(text.matchAll(URL_RE), (m) => m[0]);
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
