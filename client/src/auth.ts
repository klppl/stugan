import { reactive } from "vue";
import { connection } from "./connection";

export interface AuthState {
  ready: boolean; // /api/me has resolved
  authEnabled: boolean;
  authenticated: boolean;
  user: string;
  error: string;
  // Outer site-wide password gate ("magic word"). When enabled by
  // $STUGAN_WEB_PASSWORD on the daemon, every other endpoint is
  // blocked until the cookie is granted.
  magicRequired: boolean;
  magicGranted: boolean;
  magicError: string;
}

export const authState = reactive<AuthState>({
  ready: false,
  authEnabled: false,
  authenticated: false,
  user: "",
  error: "",
  magicRequired: false,
  magicGranted: false,
  magicError: "",
});

// canEnter reports whether the chat UI should be shown (and the socket
// connected): the magic word must be granted (if required) AND either
// auth is off or the user is logged in.
export function canEnter(): boolean {
  if (!authState.ready) return false;
  if (authState.magicRequired && !authState.magicGranted) return false;
  return !authState.authEnabled || authState.authenticated;
}

// needsMagicWord reports whether the site-wide password gate should be
// shown ahead of any other screen.
export function needsMagicWord(): boolean {
  return authState.ready && authState.magicRequired && !authState.magicGranted;
}

// Honeypot bag passed alongside login/magic-word submits. The fields
// are rendered hidden in the DOM purely to catch form-filling bots —
// see Login.vue and MagicWord.vue. The server rejects any submission
// whose honeypot is non-empty.
export interface Honeypot {
  email?: string;
  website?: string;
}

interface MeResponse {
  authEnabled: boolean;
  authenticated: boolean;
  user: string;
  magicWord?: { required: boolean; granted: boolean };
}

// refresh queries /api/me and connects the socket if allowed.
export async function refresh(): Promise<void> {
  try {
    const r = await fetch("/api/me");
    const m = (await r.json()) as MeResponse;
    authState.authEnabled = m.authEnabled;
    authState.authenticated = m.authenticated;
    authState.user = m.user;
    authState.magicRequired = !!m.magicWord?.required;
    authState.magicGranted = !!m.magicWord?.granted;
  } catch {
    // leave defaults; the socket will retry.
  }
  authState.ready = true;
  if (canEnter()) connection.connect();
}

export async function login(username: string, password: string, hp: Honeypot = {}): Promise<boolean> {
  authState.error = "";
  const r = await fetch("/api/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password, email: hp.email ?? "", website: hp.website ?? "" }),
  });
  if (!r.ok) {
    authState.error =
      r.status === 429
        ? "Too many attempts. Try again in a minute."
        : r.status === 401
          ? "Invalid username or password"
          : "Login failed";
    return false;
  }
  const j = (await r.json()) as { user: string };
  authState.authenticated = true;
  authState.user = j.user;
  connection.connect();
  return true;
}

export async function logout(): Promise<void> {
  await fetch("/api/logout", { method: "POST" });
  location.reload();
}

// submitMagicWord posts the site-wide password to the daemon. On
// success the cookie is set and the gate lifts.
export async function submitMagicWord(word: string, hp: Honeypot = {}): Promise<boolean> {
  authState.magicError = "";
  const r = await fetch("/api/magicword", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ word, email: hp.email ?? "", website: hp.website ?? "" }),
  });
  if (!r.ok) {
    authState.magicError =
      r.status === 429
        ? "Too many attempts. Try again in a minute."
        : r.status === 401
          ? "Incorrect password"
          : "Gate request failed";
    return false;
  }
  authState.magicGranted = true;
  // Re-read /api/me so the rest of the auth state (user, session) is
  // up to date, then let canEnter() drive the socket connection.
  await refresh();
  return true;
}
