// Tiny typed client for the signalwatch HTTP API. The fetch wrapper auto
// JSON-encodes bodies, attaches the configured bearer token (if any),
// unwraps {error} responses, and dispatches an auth-failed event on 401
// so the UI can flip back to the login gate.

import { getToken, clearToken, dispatchAuthFailed } from "./auth";

export type Severity = "info" | "warning" | "critical";

export type Condition =
  | { type: "threshold"; spec: { field: string; op: string; value: number } }
  | {
      type: "window_aggregate";
      spec: { field: string; agg: string; window: number; op: string; value: number };
    }
  | { type: "pattern_match"; spec: { field: string; kind: "regex" | "contains"; pattern: string } }
  | {
      type: "sql_returns_rows";
      spec: { data_source: string; query: string; min_rows: number };
    }
  | {
      type: "expression";
      spec: { expr: string; mode?: "push" | "scheduled" };
    };

export interface Rule {
  id: string;
  name: string;
  description: string;
  enabled: boolean;
  severity: Severity;
  labels: Record<string, string> | null;
  input_ref: string;
  condition: Condition;
  schedule_seconds?: number;
}

export interface ChannelBinding {
  channel: string;
  address: string;
}

export interface Subscriber {
  id: string;
  name: string;
  channels: ChannelBinding[];
}

export interface Subscription {
  id: string;
  subscriber_id: string;
  rule_id?: string;
  label_selector?: Record<string, string>;
  dwell_seconds: number;
  repeat_interval_seconds: number;
  notify_on_resolve: boolean;
  channel_filter?: string[];
}

export interface Incident {
  id: string;
  rule_id: string;
  triggered_at: string;
  resolved_at?: string;
  last_value?: string;
}

export interface LiveState {
  rule_id: string;
  state: "ok" | "firing";
  triggered_at?: string;
  last_eval_at?: string;
  last_value?: string;
  last_error?: string;
  incident_id?: string;
}

export interface Notification {
  id: string;
  incident_id: string;
  subscription_id: string;
  subscriber_id: string;
  channel: string;
  address: string;
  kind: "firing" | "repeat" | "resolved";
  sent_at: string;
  status: string;
  error?: string;
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  const token = getToken();
  if (token) {
    headers["authorization"] = `Bearer ${token}`;
  }
  const init: RequestInit = { method, headers };
  if (body !== undefined) {
    headers["content-type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  const resp = await fetch(path, init);
  if (resp.status === 401) {
    // Token went stale or server flipped on auth — clear and let
    // App.tsx swap in the login gate.
    clearToken();
    dispatchAuthFailed();
  }
  const text = await resp.text();
  if (!resp.ok) {
    let msg = text;
    try {
      msg = (JSON.parse(text) as { error?: string }).error ?? text;
    } catch {
      // raw text already
    }
    throw new Error(`${method} ${path}: ${resp.status} ${msg}`);
  }
  return text ? (JSON.parse(text) as T) : (undefined as T);
}

export const api = {
  rules: {
    list: () => request<Rule[]>("GET", "/v1/rules"),
    create: (r: Partial<Rule>) => request<Rule>("POST", "/v1/rules", r),
    update: (id: string, r: Partial<Rule>) => request<Rule>("PUT", `/v1/rules/${id}`, r),
    remove: (id: string) => request<void>("DELETE", `/v1/rules/${id}`),
    // validate compiles a candidate rule (especially the Expression
    // condition's expr-lang program) without persisting. Throws on
    // validation/compile errors with the server's message.
    validate: (r: Partial<Rule>) => request<{ ok: boolean }>("POST", "/v1/rules/validate", r),
  },
  subscribers: {
    list: () => request<Subscriber[]>("GET", "/v1/subscribers"),
    create: (s: Partial<Subscriber>) => request<Subscriber>("POST", "/v1/subscribers", s),
    remove: (id: string) => request<void>("DELETE", `/v1/subscribers/${id}`),
  },
  subscriptions: {
    list: () => request<Subscription[]>("GET", "/v1/subscriptions"),
    create: (s: Partial<Subscription>) => request<Subscription>("POST", "/v1/subscriptions", s),
    remove: (id: string) => request<void>("DELETE", `/v1/subscriptions/${id}`),
  },
  incidents: {
    list: () => request<Incident[]>("GET", "/v1/incidents"),
  },
  notifications: {
    list: () => request<Notification[]>("GET", "/v1/notifications"),
  },
  states: {
    list: () => request<LiveState[]>("GET", "/v1/states"),
  },
  events: {
    emit: (input_ref: string, record: Record<string, unknown>) =>
      request<void>("POST", "/v1/events", { input_ref, record }),
  },
};
