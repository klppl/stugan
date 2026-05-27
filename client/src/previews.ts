import { reactive } from "vue";

export interface Preview {
  url: string;
  title: string;
  description: string;
  image: string;
}

type Entry = Preview | "loading" | "error";

const cache = reactive<Record<string, Entry>>({});

// getPreview returns the cached preview/state for a URL (undefined if not yet
// requested).
export function getPreview(url: string): Entry | undefined {
  return cache[url];
}

// fetchPreview loads Open Graph metadata for a URL once, caching the result.
export async function fetchPreview(url: string): Promise<void> {
  if (cache[url]) return;
  cache[url] = "loading";
  try {
    const r = await fetch("/api/preview?url=" + encodeURIComponent(url));
    if (!r.ok) {
      cache[url] = "error";
      return;
    }
    const p = (await r.json()) as Preview;
    cache[url] = p.title || p.description || p.image ? p : "error";
  } catch {
    cache[url] = "error";
  }
}
