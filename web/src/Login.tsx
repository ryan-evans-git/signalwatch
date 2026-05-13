import { useState } from "react";
import { setToken } from "./auth";

interface LoginProps {
  onAuthenticated: () => void;
}

// Gated-login UX (chosen 2026-05-13): full-screen centered card with
// a single API-token input. No app surface renders until the user
// provides a token, when auth is required server-side. The token is
// persisted to localStorage by setToken().
export default function Login({ onAuthenticated }: LoginProps) {
  const [value, setValue] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!value.trim()) {
      setError("token is required");
      return;
    }
    setError("");
    setSubmitting(true);
    // Save the token first, then probe a gated endpoint to confirm
    // the server accepts it. /v1/states is read-only and cheap.
    setToken(value.trim());
    try {
      const resp = await fetch("/v1/states", {
        headers: { authorization: `Bearer ${value.trim()}` },
      });
      if (resp.status === 401) {
        setError("token rejected by server");
        return;
      }
      if (!resp.ok) {
        setError(`unexpected server response: ${resp.status}`);
        return;
      }
      onAuthenticated();
    } catch (err) {
      setError(`network error: ${err instanceof Error ? err.message : String(err)}`);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-slate-50 text-slate-900">
      <form
        onSubmit={handleSubmit}
        className="w-full max-w-sm rounded-lg border border-slate-200 bg-white shadow-sm p-6 space-y-4"
      >
        <header>
          <h1 className="text-lg font-semibold tracking-tight">signalwatch</h1>
          <p className="mt-1 text-sm text-slate-600">
            Enter the API token configured on this server (
            <code className="rounded bg-slate-100 px-1 py-0.5">SIGNALWATCH_API_TOKEN</code>).
          </p>
        </header>
        <label className="block">
          <span className="text-xs font-medium uppercase tracking-wide text-slate-500">
            API token
          </span>
          <input
            type="password"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            autoFocus
            autoComplete="off"
            className="mt-1 block w-full rounded-md border border-slate-300 px-3 py-2 text-sm shadow-sm focus:border-slate-500 focus:outline-none focus:ring-1 focus:ring-slate-500"
          />
        </label>
        {error && <p className="text-sm text-red-600">{error}</p>}
        <button
          type="submit"
          disabled={submitting}
          className="w-full rounded-md bg-slate-900 px-3 py-2 text-sm font-medium text-white hover:bg-slate-700 disabled:opacity-50"
        >
          {submitting ? "Verifying…" : "Sign in"}
        </button>
      </form>
    </div>
  );
}
