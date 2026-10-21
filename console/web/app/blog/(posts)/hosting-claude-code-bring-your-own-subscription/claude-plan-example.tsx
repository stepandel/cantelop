"use client";

import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import Markdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import { followStream, type RunState } from "./claude-plan-stream";

import styles from "./claude-plan-example.module.css";

const DEFAULT_PROMPT = "Create a file called hello.md in the workspace with a short note about what you are, then list the files in the workspace.";
const MODELS = [["claude-fable-5-1", "Claude Fable 5.1"], ["claude-opus-5-5", "Claude Opus 5.5"], ["claude-sonnet-5", "Claude Sonnet 5"]] as const;

type AuthState = "checking" | "signingIn" | "connected" | "failed";
type LoginState = { attemptId: string; url: string; status: string; pending: boolean };
type Turn =
  | { id: string; role: "user"; text: string }
  | { id: string; role: "note"; text: string }
  | { id: string; role: "assistant"; text: string; state: "working" | "done" | "failed"; status?: string };

type Action = "auth" | "authLogin" | "authCode" | "authCancel" | "session" | "message";

export function ClaudePlanExample() {
  const request = useRef<AbortController | null>(null);
  const [expanded, setExpanded] = useState(false);
  const [auth, setAuth] = useState<AuthState>("checking");
  const [login, setLogin] = useState<LoginState | null>(null);
  const [loginInput, setLoginInput] = useState("");
  const [model, setModel] = useState("claude-sonnet-5");
  const [prompt, setPrompt] = useState(DEFAULT_PROMPT);
  const [turns, setTurns] = useState<Turn[]>([]);
  const [error, setError] = useState<string | null>(null);
  const session = useRef<{ id: string; model: string } | null>(null);
  const thread = useRef<HTMLDivElement | null>(null);

  useEffect(() => () => request.current?.abort(), []);
  useEffect(() => { thread.current?.scrollTo({ top: thread.current.scrollHeight }); }, [turns]);

  const open = () => {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setExpanded(true); setAuth("checking"); setLogin(null); setError(null); setTurns([]); session.current = null;
    void prepare(controller);
  };

  const close = () => {
    if (auth === "signingIn" && login) {
      void call("authCancel", { attemptId: login.attemptId }).catch(() => undefined);
    }
    request.current?.abort(); request.current = null;
    setLoginInput(""); setExpanded(false);
  };

  const prepare = async (controller: AbortController) => {
    try {
      // Returns the native `claude auth login` link, or the status if the user is already signed in.
      const started = await call("authLogin", {}, controller.signal);
      if (started.authenticated === true) { setAuth("connected"); return; }
      const url = anthropicLink(started.url);
      if (typeof started.attemptId !== "string" || !url) throw new Error("The deployment returned an invalid Claude sign-in link.");
      setLogin({ attemptId: started.attemptId, url, status: "Sign in to Claude Code, then paste the code it gives you.", pending: false });
      setAuth("signingIn");
    } catch (error) {
      if (controller.signal.aborted) return;
      setAuth("failed"); setLogin(null);
      setError(error instanceof Error ? error.message : "Could not start Claude sign-in.");
    }
  };

  const submitCode = async (event: FormEvent) => {
    event.preventDefault();
    const code = loginInput.trim();
    if (!login || login.pending || !code) return;
    const controller = request.current;
    setLoginInput(""); setError(null);
    setLogin({ ...login, status: "Sending the code to Claude and confirming the sign-in…", pending: true });
    try {
      const result = await call("authCode", { attemptId: login.attemptId, code }, controller?.signal);
      if (result.authenticated !== true) throw new Error("Claude sign-in could not be confirmed. Close the demo and retry.");
      setLogin(null); setAuth("connected");
    } catch (error) {
      if (controller?.signal.aborted) return;
      if (error instanceof DemoRequestError && error.code === "code_rejected") {
        setLogin({ ...login, status: "Claude did not accept that code. Paste it again.", pending: false }); return;
      }
      setAuth("failed"); setLogin(null);
      setError(error instanceof DemoRequestError && error.code === "login_not_active"
        ? "This sign-in attempt expired. Close the demo and connect again."
        : error instanceof Error ? error.message : "Could not verify the Claude code.");
    }
  };

  const send = async (event: FormEvent) => {
    event.preventDefault();
    const text = prompt.trim();
    if (!text || busy) return;
    request.current?.abort();
    const controller = new AbortController(); request.current = controller;
    const reply = crypto.randomUUID();
    // Keep separate conversations when the user switches models.
    const fresh = session.current?.model !== model;
    const label = MODELS.find(([value]) => value === model)?.[1] ?? model;
    setPrompt("");
    setTurns((current) => [...current,
      ...(fresh && current.length ? [{ id: crypto.randomUUID(), role: "note" as const, text: `New session with ${label}` }] : []),
      { id: crypto.randomUUID(), role: "user", text },
      { id: reply, role: "assistant", text: "", state: "working", status: "Sending…" }]);
    const update = (patch: Partial<Extract<Turn, { role: "assistant" }>>) =>
      setTurns((current) => current.map((turn) => turn.id === reply && turn.role === "assistant" ? { ...turn, ...patch } : turn));
    try {
      if (fresh) {
        const created = await call("session", {}, controller.signal);
        if (typeof created.sessionId !== "string") throw new Error("The deployment returned an invalid session.");
        session.current = { id: created.sessionId, model };
      }
      const sessionId = session.current!.id;
      const stream = await openStream(sessionId, controller.signal);
      const accepted = await call("message", { sessionId, text, mode: "queue", model: session.current!.model }, controller.signal);
      if (typeof accepted.messageId !== "string") throw new Error("The deployment returned an invalid message.");
      update({ status: "Starting Claude Code" });
      await followStream(stream, accepted.messageId, controller.signal, (run) => {
        if (run.kind === "running") update({ text: run.response, status: run.status });
        else if (run.kind === "completed") update({ text: run.response || "Claude completed without a text response.", state: "done" });
        else if (run.kind === "failed") update({ text: run.message, state: "failed" });
      });
    } catch (error) {
      if (controller.signal.aborted) return;
      controller.abort();
      update({ text: error instanceof Error ? error.message : "The Claude Code session failed.", state: "failed" });
    }
  };

  const sendOnEnter = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
      event.preventDefault(); event.currentTarget.form?.requestSubmit();
    }
  };

  const busy = turns.some((turn) => turn.role === "assistant" && turn.state === "working");
  return (
    <figure className={styles.example}>
      <div className={`${styles.card} ${expanded ? styles.cardExpanded : ""}`}>
        <div className={styles.content}>
          <div className={styles.title}>Coding agent</div>
          <div className={styles.description}>Runs Claude Code in a sandbox on your own Claude subscription</div>
          <div className={styles.actions}>
            {auth === "connected" && expanded
              ? <div className={styles.models} role="group" aria-label="Claude model">{MODELS.map(([value, label]) =>
                <button key={value} type="button" aria-pressed={model === value} className={model === value ? styles.modelSelected : undefined}
                  disabled={busy} onClick={() => setModel(value)}>{label}</button>)}</div>
              : <div className={styles.models}>{MODELS.map(([value, label]) => <span key={value}>{label}</span>)}</div>}
            <button type="button" className={styles.connect} onClick={expanded ? close : open}>{expanded ? (auth === "signingIn" ? "Cancel" : "Close") : "Connect"}</button>
          </div>

          {expanded && (
            <section className={`${styles.inlineDemo} ${auth === "connected" ? styles.chatDemo : ""}`} aria-label="Live Claude Code demo">
              {auth === "checking" && <ol className={styles.steps} role="status" aria-live="polite">
                <li data-state="active"><span aria-hidden="true" /><div>Connecting to Claude Code…</div></li>
              </ol>}

              {auth === "signingIn" && login && (
                <div className={styles.loginFlow}>
                  <div className={styles.loginRow}>
                    <p className={styles.loginStatus} role="status" aria-live="polite">{login.pending && <span className={styles.spinner} aria-hidden="true" />}{login.status}</p>
                    <a className={styles.primary} href={login.url} target="_blank" rel="noopener noreferrer">Sign in to Claude Code ↗</a>
                  </div>
                  <form className={styles.codeForm} onSubmit={(event) => void submitCode(event)}>
                    <input type="password" aria-label="Code from Claude" placeholder="Paste the code from Claude" autoComplete="off" value={loginInput}
                      onChange={(event) => setLoginInput(event.target.value)} disabled={login.pending} />
                    <button className={styles.primary} type="submit" disabled={login.pending || !loginInput.trim()}>Continue</button>
                  </form>
                </div>
              )}

              {auth === "connected" && <form className={styles.sessionForm} onSubmit={(event) => void send(event)}>
                <div className={styles.thread} ref={thread} aria-live="polite">
                  {turns.length === 0 && <p className={styles.empty}>Send a message to start Claude Code on your subscription.</p>}
                  {turns.map((turn) => <Message key={turn.id} turn={turn} />)}
                </div>
                <div className={styles.composer}>
                  <textarea aria-label="Message" placeholder="Message Claude Code" value={prompt} rows={2}
                    onChange={(event) => setPrompt(event.target.value)} onKeyDown={sendOnEnter} />
                  <div className={styles.composerBar}>
                    <span className={styles.connected}><span /> Subscription connected</span>
                    <button type="submit" className={styles.primary} disabled={busy || !prompt.trim()}>Send</button>
                  </div>
                </div>
              </form>}
              {error && auth !== "connected" && <div className={styles.error} role="alert">{error}</div>}
            </section>
          )}
        </div>
      </div>
      <figcaption className={styles.caption}>Live demo: unmodified Claude Code, running on your own Claude subscription.</figcaption>
    </figure>
  );
}

function Message({ turn }: { turn: Turn }) {
  if (turn.role === "user") return <div className={styles.user}>{turn.text}</div>;
  if (turn.role === "note") return <div className={styles.note}>{turn.text}</div>;
  return (
    <div className={`${styles.assistant} ${turn.state === "failed" ? styles.failed : ""}`} role={turn.state === "failed" ? "alert" : undefined}>
      {turn.text && (turn.state === "failed"
        ? <div className={styles.plain}>{turn.text}</div>
        : <div className={styles.markdown}><Markdown remarkPlugins={[remarkGfm]} components={MARKDOWN_COMPONENTS}>{turn.text}</Markdown></div>)}
      {turn.state === "working" && <div className={styles.working}><span /> {turn.status}</div>}
    </div>
  );
}

// Claude's replies are untrusted, so links open in a new tab without referrer or opener.
const MARKDOWN_COMPONENTS: Components = {
  a: ({ node: _node, ...props }) => <a {...props} target="_blank" rel="noopener noreferrer nofollow" />,
};

async function call(action: Action, body: Record<string, unknown>, signal?: AbortSignal) {
  const response = await fetch("/api/examples/claude-plan/request", { method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ action, body }), cache: "no-store", signal });
  if (!response.ok) throw await responseError(response);
  return response.json() as Promise<Record<string, unknown>>;
}

async function openStream(sessionId: string, signal: AbortSignal): Promise<ReadableStream<Uint8Array>> {
  const response = await fetch(`/api/examples/claude-plan/events?sessionId=${encodeURIComponent(sessionId)}`,
    { cache: "no-store", signal });
  if (!response.ok || !response.body) throw await responseError(response);
  return response.body;
}

class DemoRequestError extends Error {
  constructor(message: string, readonly status: number, readonly code?: string) { super(message); }
}

async function responseError(response: Response): Promise<DemoRequestError> {
  if (response.status === 401) return new DemoRequestError("Sign in to Cantelop to use the live demo.", 401);
  if (response.status === 429) return new DemoRequestError("The live demo request limit has been reached. Try again later.", 429);
  const body = await response.json().catch(() => ({})) as { error?: string; code?: string };
  return new DemoRequestError(body.error || `The Claude deployment rejected the request (${response.status}).`, response.status, body.code);
}

function anthropicLink(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  try {
    const url = new URL(value);
    return url.protocol === "https:" && !url.username && !url.password && ["claude.ai", "claude.com", "anthropic.com"]
      .some((host) => url.hostname === host || url.hostname.endsWith(`.${host}`)) ? url.href : undefined;
  } catch { return undefined; }
}
