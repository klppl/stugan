import { reactive } from "vue";
import { connection } from "./connection";

export interface AuthState {
  ready: boolean; // /api/me has resolved
  authEnabled: boolean;
  authenticated: boolean;
  user: string;
  error: string;
}

export const authState = reactive<AuthState>({
  ready: false,
  authEnabled: false,
  authenticated: false,
  user: "",
  error: "",
});

// canEnter reports whether the chat UI should be shown (and the socket
// connected): either auth is off, or the user is logged in.
export function canEnter(): boolean {
  return authState.ready && (!authState.authEnabled || authState.authenticated);
}

// refresh queries /api/me and connects the socket if allowed.
export async function refresh(): Promise<void> {
  try {
    const r = await fetch("/api/me");
    const m = (await r.json()) as { authEnabled: boolean; authenticated: boolean; user: string };
    authState.authEnabled = m.authEnabled;
    authState.authenticated = m.authenticated;
    authState.user = m.user;
  } catch {
    // leave defaults; the socket will retry.
  }
  authState.ready = true;
  if (canEnter()) connection.connect();
}

export async function login(username: string, password: string): Promise<boolean> {
  authState.error = "";
  const r = await fetch("/api/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username, password }),
  });
  if (!r.ok) {
    authState.error = r.status === 401 ? "Invalid username or password" : "Login failed";
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
