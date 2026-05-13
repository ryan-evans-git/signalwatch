// Tiny shared-token auth helper for the signalwatch UI.
//
// The server enables auth via $SIGNALWATCH_API_TOKEN at startup; the UI
// asks /v1/auth-status whether auth is required and then either renders
// the gate (Login.tsx) or proceeds straight to the app. The token is
// persisted to localStorage and attached to every /v1/* fetch.

const STORAGE_KEY = "signalwatch.api_token";

export function getToken(): string {
  return localStorage.getItem(STORAGE_KEY) ?? "";
}

export function setToken(value: string): void {
  localStorage.setItem(STORAGE_KEY, value);
}

export function clearToken(): void {
  localStorage.removeItem(STORAGE_KEY);
}

export interface AuthStatus {
  auth_required: boolean;
}

// Probe the server's auth state. Open endpoint, no token required.
export async function fetchAuthStatus(): Promise<AuthStatus> {
  const resp = await fetch("/v1/auth-status");
  if (!resp.ok) {
    throw new Error(`/v1/auth-status: ${resp.status}`);
  }
  return resp.json();
}

// authFailedEvent is dispatched whenever a /v1/* fetch returns 401.
// App.tsx listens for it and flips back to the login gate.
export const AUTH_FAILED_EVENT = "signalwatch:auth-failed";

export function dispatchAuthFailed(): void {
  window.dispatchEvent(new CustomEvent(AUTH_FAILED_EVENT));
}
