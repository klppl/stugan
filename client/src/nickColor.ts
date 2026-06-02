// Deterministic per-nick coloring. A nick hashes into three independent
// dimensions — hue, saturation and lightness — each kept in a range that
// stays legible on both the dark and light themes (mid lightness, moderate
// saturation). Varying all three (rather than hue alone) spreads nicks across
// far more perceptually-distinct colors: HSL hue is not perceptually uniform,
// so neighbouring hues alone collide easily. Case- and
// trailing-underscore-insensitive so "alice", "Alice" and "alice_" share a
// color, matching the irssi/weechat habit where those are the same person.

const cache = new Map<string, string>();

// canonical strips a trailing run of the common "away/ghost" suffixes so a
// nick and its variants color alike.
function canonical(nick: string): string {
  return nick.toLowerCase().replace(/[_`|^-]+$/, "") || nick.toLowerCase();
}

// fnv1a is a small, fast, well-distributed string hash (32-bit).
function fnv1a(s: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

export function nickColor(nick: string): string {
  if (!nick) return "inherit";
  const key = canonical(nick);
  const hit = cache.get(key);
  if (hit) return hit;
  // Decorrelate the three dimensions by hashing salted variants of the key,
  // so two nicks that happen to share a hue still differ in saturation or
  // lightness. Ranges are chosen to stay readable on both themes.
  const hue = fnv1a(key) % 360;
  const sat = 50 + (fnv1a(key + "\x01") % 35); // 50–84%
  const light = 55 + (fnv1a(key + "\x02") % 20); // 55–74%
  const color = `hsl(${hue}, ${sat}%, ${light}%)`;
  cache.set(key, color);
  return color;
}
