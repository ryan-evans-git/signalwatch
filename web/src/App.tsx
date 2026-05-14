import { useEffect, useState } from "react";
import { api, type LiveState, type Rule, type Subscriber, type Subscription, type Incident, type Notification as Note } from "./api";
import { AUTH_FAILED_EVENT, fetchAuthStatus, getToken } from "./auth";
import Login from "./Login";

type TabId = "rules" | "subscribers" | "subscriptions" | "incidents" | "states" | "emit";

const TABS: { id: TabId; label: string }[] = [
  { id: "rules", label: "Rules" },
  { id: "subscribers", label: "Subscribers" },
  { id: "subscriptions", label: "Subscriptions" },
  { id: "incidents", label: "Incidents" },
  { id: "states", label: "Live State" },
  { id: "emit", label: "Emit Event" },
];

// AuthState models the three possible gate states:
//   loading: probing /v1/auth-status
//   open:    server doesn't require auth, OR token is present
//   gated:   auth required and no token — show <Login/>
type AuthState = "loading" | "open" | "gated";

export default function App() {
  const [auth, setAuth] = useState<AuthState>("loading");

  // On mount, probe the server's auth state. If auth is required and
  // we already have a stored token, optimistically render the app —
  // api.ts will dispatch AUTH_FAILED_EVENT on 401 if the token is
  // stale, which flips us back to the gate.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const status = await fetchAuthStatus();
        if (cancelled) return;
        if (!status.auth_required || getToken()) {
          setAuth("open");
        } else {
          setAuth("gated");
        }
      } catch {
        // If the probe itself fails (network blip), fall back to the
        // gate — a user who supplies a token can try again.
        if (!cancelled) setAuth("gated");
      }
    })();
    const onFailed = () => setAuth("gated");
    window.addEventListener(AUTH_FAILED_EVENT, onFailed);
    return () => {
      cancelled = true;
      window.removeEventListener(AUTH_FAILED_EVENT, onFailed);
    };
  }, []);

  if (auth === "loading") {
    return (
      <div className="min-h-screen flex items-center justify-center bg-slate-50 text-slate-500 text-sm">
        Loading…
      </div>
    );
  }
  if (auth === "gated") {
    return <Login onAuthenticated={() => setAuth("open")} />;
  }
  return <AppShell />;
}

// useHash reflects window.location.hash and updates on every change.
// Hash routing keeps the SPA dependency-free; the only hash route we
// recognize is "#/rules/:id" for the per-rule drill-down.
function useHash() {
  const [hash, setHash] = useState<string>(window.location.hash);
  useEffect(() => {
    const handler = () => setHash(window.location.hash);
    window.addEventListener("hashchange", handler);
    return () => window.removeEventListener("hashchange", handler);
  }, []);
  return hash;
}

function parseRuleDetailRoute(hash: string): string | null {
  // "#/rules/:id" → id
  const m = hash.match(/^#\/rules\/([^/?#]+)$/);
  return m ? decodeURIComponent(m[1]) : null;
}

function AppShell() {
  const [tab, setTab] = useState<TabId>("rules");
  const hash = useHash();
  const ruleDetailID = parseRuleDetailRoute(hash);
  return (
    <div className="min-h-screen bg-slate-50 text-slate-900">
      <header className="border-b border-slate-200 bg-white">
        <div className="mx-auto max-w-6xl px-6 py-4 flex items-baseline gap-4">
          <h1 className="text-xl font-semibold tracking-tight">signalwatch</h1>
          <span className="text-xs uppercase text-slate-400 tracking-widest">pre-release</span>
          <span className="flex-1" />
          {/*
            API Docs link sends humans to the embedded Swagger UI at
            /docs. Anchor tag (not a tab) because /docs is a separate
            page outside the SPA's hash-router.
          */}
          <a
            href="/docs"
            target="_blank"
            rel="noopener"
            className="text-sm text-slate-500 hover:text-slate-700 hover:underline"
            title="OpenAPI 3.1 spec rendered with Swagger UI"
          >
            API docs ↗
          </a>
        </div>
        <nav className="mx-auto max-w-6xl px-6 -mb-px flex gap-2">
          {TABS.map((t) => (
            <button
              key={t.id}
              onClick={() => {
                // Leaving the detail route clears the hash so the tab
                // strip is unambiguous.
                if (ruleDetailID) window.location.hash = "";
                setTab(t.id);
              }}
              className={
                "px-3 py-2 text-sm border-b-2 -mb-px transition " +
                (tab === t.id && !ruleDetailID
                  ? "border-slate-900 text-slate-900 font-medium"
                  : "border-transparent text-slate-500 hover:text-slate-700")
              }
            >
              {t.label}
            </button>
          ))}
        </nav>
      </header>
      <main className="mx-auto max-w-6xl px-6 py-8">
        {ruleDetailID ? (
          <RuleDetailPanel id={ruleDetailID} />
        ) : (
          <>
            {tab === "rules" && <RulesPanel />}
            {tab === "subscribers" && <SubscribersPanel />}
            {tab === "subscriptions" && <SubscriptionsPanel />}
            {tab === "incidents" && <IncidentsPanel />}
            {tab === "states" && <StatesPanel />}
            {tab === "emit" && <EmitPanel />}
          </>
        )}
      </main>
    </div>
  );
}

function Card({ title, action, children }: { title: string; action?: React.ReactNode; children: React.ReactNode }) {
  return (
    <section className="rounded-lg border border-slate-200 bg-white shadow-sm">
      <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3">
        <h2 className="font-medium">{title}</h2>
        {action}
      </div>
      <div className="p-4">{children}</div>
    </section>
  );
}

function Pill({ tone, children }: { tone: "ok" | "firing" | "warning" | "critical" | "info" | "neutral"; children: React.ReactNode }) {
  const map: Record<typeof tone, string> = {
    ok: "bg-emerald-100 text-emerald-800",
    firing: "bg-rose-100 text-rose-800",
    warning: "bg-amber-100 text-amber-800",
    critical: "bg-rose-100 text-rose-800",
    info: "bg-sky-100 text-sky-800",
    neutral: "bg-slate-100 text-slate-700",
  };
  return <span className={"inline-block rounded px-2 py-0.5 text-xs font-medium " + map[tone]}>{children}</span>;
}

function ErrorBanner({ err }: { err: string | null }) {
  if (!err) return null;
  return <div className="mb-4 rounded border border-rose-200 bg-rose-50 px-3 py-2 text-sm text-rose-700">{err}</div>;
}

// ---------- Rules ----------

function RulesPanel() {
  const [rules, setRules] = useState<Rule[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const reload = () => api.rules.list().then(setRules).catch((e) => setErr(String(e)));
  useEffect(() => { reload(); }, []);

  return (
    <Card
      title={`Rules (${rules.length})`}
      action={
        <button onClick={() => setCreating((v) => !v)} className="rounded bg-slate-900 px-3 py-1.5 text-sm text-white hover:bg-slate-700">
          {creating ? "Cancel" : "+ New rule"}
        </button>
      }
    >
      <ErrorBanner err={err} />
      {creating && <NewRuleForm onCreated={() => { setCreating(false); reload(); }} />}
      <div className="overflow-x-auto">
        <table className="min-w-full text-sm">
          <thead>
            <tr className="text-left text-xs uppercase text-slate-400">
              <th className="py-2 pr-4">Name</th>
              <th className="py-2 pr-4">Severity</th>
              <th className="py-2 pr-4">Input</th>
              <th className="py-2 pr-4">Condition</th>
              <th className="py-2 pr-4">Enabled</th>
              <th className="py-2 pr-4"></th>
            </tr>
          </thead>
          <tbody>
            {rules.map((r) => (
              <tr key={r.id} className="border-t border-slate-100">
                <td className="py-2 pr-4 font-medium">{r.name}</td>
                <td className="py-2 pr-4">
                  <Pill tone={r.severity === "critical" ? "critical" : r.severity === "warning" ? "warning" : "info"}>
                    {r.severity}
                  </Pill>
                </td>
                <td className="py-2 pr-4">{r.input_ref}</td>
                <td className="py-2 pr-4 text-xs text-slate-500 font-mono">{summarizeCondition(r)}</td>
                <td className="py-2 pr-4">{r.enabled ? <Pill tone="ok">on</Pill> : <Pill tone="neutral">off</Pill>}</td>
                <td className="py-2 pr-4 text-right space-x-3">
                  <a
                    href={`#/rules/${encodeURIComponent(r.id)}`}
                    className="text-xs text-slate-600 hover:underline"
                  >
                    view
                  </a>
                  <button
                    onClick={async () => { await api.rules.remove(r.id); reload(); }}
                    className="text-xs text-rose-600 hover:underline"
                  >
                    delete
                  </button>
                </td>
              </tr>
            ))}
            {rules.length === 0 && (
              <tr><td colSpan={6} className="py-6 text-center text-slate-400">No rules yet.</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

function summarizeCondition(r: Rule): string {
  const c = r.condition as Rule["condition"];
  switch (c.type) {
    case "threshold":
      return `${c.spec.field} ${c.spec.op} ${c.spec.value}`;
    case "window_aggregate":
      return `${c.spec.agg}(${c.spec.field}, ${c.spec.window}ns) ${c.spec.op} ${c.spec.value}`;
    case "pattern_match":
      return `${c.spec.field} ${c.spec.kind} "${c.spec.pattern}"`;
    case "sql_returns_rows":
      return `sql(${c.spec.data_source}) >= ${c.spec.min_rows}`;
    case "expression": {
      const truncated = c.spec.expr.length > 60 ? c.spec.expr.slice(0, 59) + "…" : c.spec.expr;
      return `expr${c.spec.mode === "scheduled" ? "@scheduled" : ""}: ${truncated}`;
    }
  }
}

type ConditionKind = "pattern_match" | "expression";

function NewRuleForm({ onCreated }: { onCreated: () => void }) {
  const [name, setName] = useState("");
  const [inputRef, setInputRef] = useState("events");
  const [severity, setSeverity] = useState<"info" | "warning" | "critical">("warning");
  const [kind, setKind] = useState<ConditionKind>("pattern_match");

  // Pattern-match fields.
  const [field, setField] = useState("level");
  const [pattern, setPattern] = useState("ERROR");

  // Expression fields.
  const [expr, setExpr] = useState(`record.level == "ERROR"`);
  const [exprMode, setExprMode] = useState<"push" | "scheduled">("push");
  const [scheduleSeconds, setScheduleSeconds] = useState<number>(300);
  const [validateMsg, setValidateMsg] = useState<{ ok: boolean; text: string } | null>(null);

  const [err, setErr] = useState<string | null>(null);

  function buildPayload(): Partial<Rule> {
    if (kind === "expression") {
      const payload: Partial<Rule> = {
        name,
        enabled: true,
        severity,
        input_ref: inputRef,
        condition: {
          type: "expression",
          spec: { expr, mode: exprMode },
        } as Rule["condition"],
      };
      if (exprMode === "scheduled") {
        payload.schedule_seconds = scheduleSeconds;
      }
      return payload;
    }
    return {
      name,
      enabled: true,
      severity,
      input_ref: inputRef,
      condition: { type: "pattern_match", spec: { field, kind: "contains", pattern } },
    };
  }

  async function validate() {
    setValidateMsg(null);
    try {
      await api.rules.validate(buildPayload());
      setValidateMsg({ ok: true, text: "Compiles cleanly." });
    } catch (e) {
      setValidateMsg({ ok: false, text: String(e) });
    }
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setErr(null);
    try {
      await api.rules.create(buildPayload());
      onCreated();
    } catch (e) {
      setErr(String(e));
    }
  }

  return (
    <form onSubmit={submit} className="mb-4 grid grid-cols-2 gap-3 rounded border border-slate-200 bg-slate-50 p-4">
      <ErrorBanner err={err} />
      <Field label="Name"><input className="input" value={name} onChange={(e) => setName(e.target.value)} required /></Field>
      <Field label="Input ref"><input className="input" value={inputRef} onChange={(e) => setInputRef(e.target.value)} /></Field>
      <Field label="Severity">
        <select className="input" value={severity} onChange={(e) => setSeverity(e.target.value as typeof severity)}>
          <option value="info">info</option>
          <option value="warning">warning</option>
          <option value="critical">critical</option>
        </select>
      </Field>
      <Field label="Condition type">
        <select className="input" value={kind} onChange={(e) => setKind(e.target.value as ConditionKind)}>
          <option value="pattern_match">Pattern match</option>
          <option value="expression">Expression</option>
        </select>
      </Field>
      {kind === "pattern_match" && (
        <>
          <Field label="Field"><input className="input" value={field} onChange={(e) => setField(e.target.value)} /></Field>
          <Field label="Substring pattern"><input className="input" value={pattern} onChange={(e) => setPattern(e.target.value)} /></Field>
        </>
      )}
      {kind === "expression" && (
        <>
          <Field label="Expression mode">
            <select className="input" value={exprMode} onChange={(e) => setExprMode(e.target.value as typeof exprMode)}>
              <option value="push">push (per record)</option>
              <option value="scheduled">scheduled (interval)</option>
            </select>
          </Field>
          {exprMode === "scheduled" && (
            <Field label="Schedule (seconds)">
              <input
                type="number"
                min={1}
                className="input"
                value={scheduleSeconds}
                onChange={(e) => setScheduleSeconds(Number(e.target.value))}
              />
            </Field>
          )}
          <label className="col-span-2 flex flex-col gap-1 text-sm">
            <span className="text-xs uppercase text-slate-400 tracking-wide">Expression</span>
            <textarea
              className="input font-mono"
              rows={4}
              value={expr}
              onChange={(e) => setExpr(e.target.value)}
              placeholder={`record.level == "ERROR"  or  avg_over("mpg", "30d") < 5`}
            />
          </label>
          {validateMsg && (
            <div className={`col-span-2 rounded px-3 py-2 text-xs ${validateMsg.ok ? "bg-green-50 text-green-700" : "bg-rose-50 text-rose-700"}`}>
              {validateMsg.text}
            </div>
          )}
        </>
      )}
      <div className="col-span-2 flex justify-end gap-2">
        {kind === "expression" && (
          <button type="button" onClick={validate} className="rounded border border-slate-300 bg-white px-3 py-1.5 text-sm hover:bg-slate-100">
            Validate
          </button>
        )}
        <button className="rounded bg-slate-900 px-3 py-1.5 text-sm text-white hover:bg-slate-700">Create rule</button>
      </div>
    </form>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1 text-sm">
      <span className="text-xs uppercase text-slate-400 tracking-wide">{label}</span>
      {children}
    </label>
  );
}

// ---------- Subscribers ----------

function SubscribersPanel() {
  const [rows, setRows] = useState<Subscriber[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const reload = () => api.subscribers.list().then(setRows).catch((e) => setErr(String(e)));
  useEffect(() => { reload(); }, []);

  return (
    <Card
      title={`Subscribers (${rows.length})`}
      action={
        <button onClick={() => setCreating((v) => !v)} className="rounded bg-slate-900 px-3 py-1.5 text-sm text-white hover:bg-slate-700">
          {creating ? "Cancel" : "+ New subscriber"}
        </button>
      }
    >
      <ErrorBanner err={err} />
      {creating && <NewSubscriberForm onCreated={() => { setCreating(false); reload(); }} />}
      <div className="overflow-x-auto">
        <table className="min-w-full text-sm">
          <thead>
            <tr className="text-left text-xs uppercase text-slate-400">
              <th className="py-2 pr-4">Name</th>
              <th className="py-2 pr-4">Channels</th>
              <th className="py-2 pr-4"></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((s) => (
              <tr key={s.id} className="border-t border-slate-100">
                <td className="py-2 pr-4 font-medium">{s.name}</td>
                <td className="py-2 pr-4">
                  {s.channels?.map((c, i) => (
                    <span key={i} className="mr-2 font-mono text-xs">{c.channel}: {c.address}</span>
                  ))}
                </td>
                <td className="py-2 pr-4 text-right">
                  <button onClick={async () => { await api.subscribers.remove(s.id); reload(); }} className="text-xs text-rose-600 hover:underline">delete</button>
                </td>
              </tr>
            ))}
            {rows.length === 0 && <tr><td colSpan={3} className="py-6 text-center text-slate-400">No subscribers yet.</td></tr>}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

function NewSubscriberForm({ onCreated }: { onCreated: () => void }) {
  const [name, setName] = useState("");
  const [channelName, setChannelName] = useState("");
  const [address, setAddress] = useState("");
  const [err, setErr] = useState<string | null>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    try {
      await api.subscribers.create({ name, channels: [{ channel: channelName, address }] });
      onCreated();
    } catch (e) { setErr(String(e)); }
  }

  return (
    <form onSubmit={submit} className="mb-4 grid grid-cols-2 gap-3 rounded border border-slate-200 bg-slate-50 p-4">
      <ErrorBanner err={err} />
      <Field label="Name"><input className="input" value={name} onChange={(e) => setName(e.target.value)} required /></Field>
      <Field label="Channel name (e.g. ops-email)"><input className="input" value={channelName} onChange={(e) => setChannelName(e.target.value)} required /></Field>
      <Field label="Address"><input className="input col-span-2" value={address} onChange={(e) => setAddress(e.target.value)} /></Field>
      <div className="col-span-2 flex justify-end">
        <button className="rounded bg-slate-900 px-3 py-1.5 text-sm text-white hover:bg-slate-700">Create</button>
      </div>
    </form>
  );
}

// ---------- Subscriptions ----------

function SubscriptionsPanel() {
  const [rows, setRows] = useState<Subscription[]>([]);
  const [rules, setRules] = useState<Rule[]>([]);
  const [subs, setSubs] = useState<Subscriber[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const reload = () => Promise.all([api.subscriptions.list(), api.rules.list(), api.subscribers.list()])
    .then(([a, b, c]) => { setRows(a); setRules(b); setSubs(c); })
    .catch((e) => setErr(String(e)));
  useEffect(() => { reload(); }, []);

  const ruleName = (id: string) => rules.find((r) => r.id === id)?.name ?? id;
  const subName = (id: string) => subs.find((s) => s.id === id)?.name ?? id;

  return (
    <Card
      title={`Subscriptions (${rows.length})`}
      action={
        <button onClick={() => setCreating((v) => !v)} className="rounded bg-slate-900 px-3 py-1.5 text-sm text-white hover:bg-slate-700">
          {creating ? "Cancel" : "+ New subscription"}
        </button>
      }
    >
      <ErrorBanner err={err} />
      {creating && <NewSubscriptionForm rules={rules} subs={subs} onCreated={() => { setCreating(false); reload(); }} />}
      <div className="overflow-x-auto">
        <table className="min-w-full text-sm">
          <thead>
            <tr className="text-left text-xs uppercase text-slate-400">
              <th className="py-2 pr-4">Subscriber</th>
              <th className="py-2 pr-4">Rule</th>
              <th className="py-2 pr-4">Dwell</th>
              <th className="py-2 pr-4">Repeat</th>
              <th className="py-2 pr-4">Resolve?</th>
              <th className="py-2 pr-4">Mode</th>
              <th className="py-2 pr-4"></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((s) => (
              <tr key={s.id} className="border-t border-slate-100">
                <td className="py-2 pr-4 font-medium">{subName(s.subscriber_id)}</td>
                <td className="py-2 pr-4">{s.rule_id ? ruleName(s.rule_id) : <em>label selector</em>}</td>
                <td className="py-2 pr-4">{s.dwell_seconds || 0}s</td>
                <td className="py-2 pr-4">{s.repeat_interval_seconds || 0}s</td>
                <td className="py-2 pr-4">{s.notify_on_resolve ? <Pill tone="ok">yes</Pill> : <Pill tone="neutral">no</Pill>}</td>
                <td className="py-2 pr-4">{s.one_shot ? <Pill tone="ok">one-shot</Pill> : <Pill tone="neutral">recurring</Pill>}</td>
                <td className="py-2 pr-4 text-right">
                  <button onClick={async () => { await api.subscriptions.remove(s.id); reload(); }} className="text-xs text-rose-600 hover:underline">delete</button>
                </td>
              </tr>
            ))}
            {rows.length === 0 && <tr><td colSpan={7} className="py-6 text-center text-slate-400">No subscriptions yet.</td></tr>}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

function NewSubscriptionForm({ rules, subs, onCreated }: { rules: Rule[]; subs: Subscriber[]; onCreated: () => void }) {
  const [subscriberId, setSubscriberId] = useState(subs[0]?.id ?? "");
  const [ruleId, setRuleId] = useState(rules[0]?.id ?? "");
  const [dwell, setDwell] = useState(0);
  const [repeat, setRepeat] = useState(0);
  const [resolve, setResolve] = useState(true);
  const [oneShot, setOneShot] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    try {
      await api.subscriptions.create({
        subscriber_id: subscriberId,
        rule_id: ruleId,
        dwell_seconds: Number(dwell),
        repeat_interval_seconds: Number(repeat),
        notify_on_resolve: resolve,
        one_shot: oneShot,
      });
      onCreated();
    } catch (e) { setErr(String(e)); }
  }

  return (
    <form onSubmit={submit} className="mb-4 grid grid-cols-2 gap-3 rounded border border-slate-200 bg-slate-50 p-4">
      <ErrorBanner err={err} />
      <Field label="Subscriber">
        <select className="input" value={subscriberId} onChange={(e) => setSubscriberId(e.target.value)}>
          {subs.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
        </select>
      </Field>
      <Field label="Rule">
        <select className="input" value={ruleId} onChange={(e) => setRuleId(e.target.value)}>
          {rules.map((r) => <option key={r.id} value={r.id}>{r.name}</option>)}
        </select>
      </Field>
      <Field label="Dwell (seconds)"><input type="number" className="input" value={dwell} onChange={(e) => setDwell(Number(e.target.value))} /></Field>
      <Field label="Repeat interval (seconds, 0 = never)"><input type="number" className="input" value={repeat} onChange={(e) => setRepeat(Number(e.target.value))} /></Field>
      <label className="col-span-2 flex items-center gap-2 text-sm">
        <input type="checkbox" checked={resolve} onChange={(e) => setResolve(e.target.checked)} />
        Notify on resolve
      </label>
      <label className="col-span-2 flex items-center gap-2 text-sm">
        <input type="checkbox" checked={oneShot} onChange={(e) => setOneShot(e.target.checked)} />
        One-time notification (fire once, ever; no repeats, no resolve ping, no new-incident refires)
      </label>
      <div className="col-span-2 flex justify-end">
        <button className="rounded bg-slate-900 px-3 py-1.5 text-sm text-white hover:bg-slate-700">Create</button>
      </div>
    </form>
  );
}

// ---------- Incidents ----------

function IncidentsPanel() {
  const [rows, setRows] = useState<Incident[]>([]);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    const tick = () => api.incidents.list().then(setRows).catch((e) => setErr(String(e)));
    tick();
    const id = setInterval(tick, 3000);
    return () => clearInterval(id);
  }, []);

  return (
    <Card title={`Incidents (${rows.length})`}>
      <ErrorBanner err={err} />
      <div className="overflow-x-auto">
        <table className="min-w-full text-sm">
          <thead>
            <tr className="text-left text-xs uppercase text-slate-400">
              <th className="py-2 pr-4">ID</th>
              <th className="py-2 pr-4">Rule</th>
              <th className="py-2 pr-4">Triggered</th>
              <th className="py-2 pr-4">Resolved</th>
              <th className="py-2 pr-4">Last value</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((i) => (
              <tr key={i.id} className="border-t border-slate-100 font-mono text-xs">
                <td className="py-2 pr-4">{i.id.slice(0, 8)}</td>
                <td className="py-2 pr-4">{i.rule_id.slice(0, 8)}</td>
                <td className="py-2 pr-4">{i.triggered_at}</td>
                <td className="py-2 pr-4">{i.resolved_at && i.resolved_at !== "0001-01-01T00:00:00Z" ? i.resolved_at : <Pill tone="firing">open</Pill>}</td>
                <td className="py-2 pr-4">{i.last_value}</td>
              </tr>
            ))}
            {rows.length === 0 && <tr><td colSpan={5} className="py-6 text-center text-slate-400">No incidents yet.</td></tr>}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

// ---------- Rule detail (drill-down) ----------

function RuleDetailPanel({ id }: { id: string }) {
  const [rule, setRule] = useState<Rule | null>(null);
  const [incidents, setIncidents] = useState<Incident[]>([]);
  const [notesByIncident, setNotesByIncident] = useState<Record<string, Note[]>>({});
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const [r, incs] = await Promise.all([
          api.rules.get(id),
          api.incidents.listForRule(id),
        ]);
        if (cancelled) return;
        setRule(r);
        setIncidents(incs);
        // Fetch notifications for each incident in parallel.
        const entries = await Promise.all(
          incs.map(async (inc) => {
            try {
              const detail = await api.incidents.get(inc.id);
              return [inc.id, detail.notifications ?? []] as const;
            } catch {
              return [inc.id, [] as Note[]] as const;
            }
          }),
        );
        if (cancelled) return;
        const map: Record<string, Note[]> = {};
        for (const [k, v] of entries) map[k] = v;
        setNotesByIncident(map);
      } catch (e) {
        if (!cancelled) setErr(String(e));
      }
    })();
    return () => { cancelled = true; };
  }, [id]);

  function exportCSV() {
    const params = new URLSearchParams({ rule_id: id, format: "csv" });
    window.open(`/v1/incidents/export?${params}`, "_blank");
  }

  return (
    <div className="space-y-4">
      <div className="flex items-baseline gap-3">
        <a href="#" className="text-sm text-slate-500 hover:underline">← Rules</a>
        <h2 className="text-lg font-semibold">{rule?.name ?? id}</h2>
        {rule?.severity && <Pill tone={rule.severity === "critical" ? "critical" : rule.severity === "warning" ? "warning" : "info"}>{rule.severity}</Pill>}
        <span className="flex-1" />
        <button
          onClick={exportCSV}
          className="rounded border border-slate-300 bg-white px-3 py-1.5 text-xs hover:bg-slate-100"
        >
          Export incidents CSV
        </button>
      </div>
      <ErrorBanner err={err} />
      {rule && (
        <Card title="Rule">
          <dl className="grid grid-cols-2 gap-x-6 gap-y-1 text-sm">
            <dt className="text-slate-500">Input</dt><dd className="font-mono text-xs">{rule.input_ref}</dd>
            <dt className="text-slate-500">Condition</dt><dd className="font-mono text-xs">{summarizeCondition(rule)}</dd>
            <dt className="text-slate-500">Enabled</dt><dd>{rule.enabled ? "yes" : "no"}</dd>
            <dt className="text-slate-500">Schedule</dt><dd>{rule.schedule_seconds ? `${rule.schedule_seconds}s` : "push"}</dd>
          </dl>
        </Card>
      )}
      <Card title={`Incidents (${incidents.length})`}>
        {incidents.length === 0 ? (
          <p className="text-sm text-slate-400">No incidents recorded for this rule yet.</p>
        ) : (
          <ul className="space-y-3">
            {incidents.map((inc) => {
              const notes = notesByIncident[inc.id] ?? [];
              // Go's omitempty doesn't skip zero time.Time, so unresolved
              // incidents arrive over the wire as "0001-01-01T...". Treat
              // that as unresolved.
              const resolved = inc.resolved_at && !inc.resolved_at.startsWith("0001-") ? inc.resolved_at : "";
              return (
                <li key={inc.id} className="rounded border border-slate-200 p-3">
                  <div className="flex items-center gap-2 text-sm">
                    <Pill tone={resolved ? "ok" : "critical"}>{resolved ? "resolved" : "firing"}</Pill>
                    <span className="text-xs font-mono text-slate-500">{inc.id}</span>
                    <span className="flex-1" />
                    <span className="text-xs text-slate-500">triggered {fmtTime(inc.triggered_at)}</span>
                    {resolved && <span className="text-xs text-slate-500">· resolved {fmtTime(resolved)}</span>}
                  </div>
                  {inc.last_value && <p className="mt-1 text-xs text-slate-500 font-mono">{inc.last_value}</p>}
                  {notes.length > 0 && (
                    <ul className="mt-2 space-y-1 text-xs">
                      {notes.map((n) => (
                        <li key={n.id} className="flex items-center gap-2">
                          <Pill tone={n.status === "ok" ? "ok" : "critical"}>{n.kind}</Pill>
                          <span className="text-slate-600">{n.channel} → {n.address}</span>
                          <span className="flex-1" />
                          <span className="text-slate-400">{fmtTime(n.sent_at)}</span>
                        </li>
                      ))}
                    </ul>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </Card>
    </div>
  );
}

function fmtTime(s?: string): string {
  if (!s) return "";
  try {
    return new Date(s).toLocaleString();
  } catch {
    return s;
  }
}

// ---------- Live State ----------

function StatesPanel() {
  const [rows, setRows] = useState<LiveState[]>([]);
  const [rules, setRules] = useState<Rule[]>([]);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    const tick = () => Promise.all([api.states.list(), api.rules.list()])
      .then(([s, r]) => { setRows(s); setRules(r); })
      .catch((e) => setErr(String(e)));
    tick();
    const id = setInterval(tick, 2000);
    return () => clearInterval(id);
  }, []);
  const ruleName = (id: string) => rules.find((r) => r.id === id)?.name ?? id;

  return (
    <Card title={`Live State (${rows.length})`}>
      <ErrorBanner err={err} />
      <div className="overflow-x-auto">
        <table className="min-w-full text-sm">
          <thead>
            <tr className="text-left text-xs uppercase text-slate-400">
              <th className="py-2 pr-4">Rule</th>
              <th className="py-2 pr-4">State</th>
              <th className="py-2 pr-4">Triggered</th>
              <th className="py-2 pr-4">Last evaluated</th>
              <th className="py-2 pr-4">Last value</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((s) => (
              <tr key={s.rule_id} className="border-t border-slate-100">
                <td className="py-2 pr-4 font-medium">{ruleName(s.rule_id)}</td>
                <td className="py-2 pr-4">
                  {s.state === "firing" ? <Pill tone="firing">firing</Pill> : <Pill tone="ok">ok</Pill>}
                </td>
                <td className="py-2 pr-4 text-xs text-slate-500">{s.triggered_at}</td>
                <td className="py-2 pr-4 text-xs text-slate-500">{s.last_eval_at}</td>
                <td className="py-2 pr-4 font-mono text-xs">{s.last_value}</td>
              </tr>
            ))}
            {rows.length === 0 && <tr><td colSpan={5} className="py-6 text-center text-slate-400">No state yet — push an event to start.</td></tr>}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

// ---------- Emit ----------

function EmitPanel() {
  const [inputRef, setInputRef] = useState("events");
  const [body, setBody] = useState('{ "level": "ERROR", "msg": "test" }');
  const [msg, setMsg] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setMsg(null); setErr(null);
    try {
      const record = JSON.parse(body);
      await api.events.emit(inputRef, record);
      setMsg("Event accepted.");
    } catch (e) {
      setErr(String(e));
    }
  }

  return (
    <Card title="Emit Event">
      <form onSubmit={submit} className="grid grid-cols-1 gap-3">
        <ErrorBanner err={err} />
        {msg && <div className="rounded border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm text-emerald-700">{msg}</div>}
        <Field label="Input ref"><input className="input" value={inputRef} onChange={(e) => setInputRef(e.target.value)} /></Field>
        <Field label="Record (JSON)">
          <textarea className="input font-mono text-xs h-40" value={body} onChange={(e) => setBody(e.target.value)} />
        </Field>
        <div className="flex justify-end">
          <button className="rounded bg-slate-900 px-3 py-1.5 text-sm text-white hover:bg-slate-700">Emit</button>
        </div>
      </form>
    </Card>
  );
}
