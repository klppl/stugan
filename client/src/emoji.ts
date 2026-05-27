// A small :shortcode: → emoji table for autocomplete. Not exhaustive; the
// common ones cover most chat use.
export const EMOJI: Record<string, string> = {
  smile: "😄",
  grin: "😁",
  joy: "😂",
  rofl: "🤣",
  wink: "😉",
  thinking: "🤔",
  facepalm: "🤦",
  shrug: "🤷",
  cry: "😢",
  sob: "😭",
  rage: "😡",
  cool: "😎",
  heart: "❤️",
  fire: "🔥",
  tada: "🎉",
  rocket: "🚀",
  eyes: "👀",
  wave: "👋",
  pray: "🙏",
  clap: "👏",
  thumbsup: "👍",
  thumbsdown: "👎",
  ok_hand: "👌",
  muscle: "💪",
  check: "✅",
  x: "❌",
  warning: "⚠️",
  bug: "🐛",
  sparkles: "✨",
  star: "⭐",
  zzz: "💤",
  coffee: "☕",
  beer: "🍺",
  pizza: "🍕",
  poop: "💩",
  ghost: "👻",
  robot: "🤖",
  100: "💯",
};

// emojiMatches returns shortcodes starting with the given prefix.
export function emojiMatches(prefix: string, limit = 8): { code: string; char: string }[] {
  const out: { code: string; char: string }[] = [];
  for (const code of Object.keys(EMOJI)) {
    if (code.startsWith(prefix)) {
      out.push({ code, char: EMOJI[code] });
      if (out.length >= limit) break;
    }
  }
  return out;
}

// replaceEmoji turns :shortcode: sequences in text into emoji characters.
export function replaceEmoji(text: string): string {
  return text.replace(/:([a-z0-9_+]+):/g, (m, code) => EMOJI[code] ?? m);
}
