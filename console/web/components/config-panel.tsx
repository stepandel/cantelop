"use client";

import { type FormEvent, type ReactNode, useState } from "react";

import {
  type ConfigScope,
  type ConfigValue,
  type ConfigValueKind,
  type EnvironmentDeclarations,
  type EnvironmentStatus,
  configNamePattern,
  declaredKind,
  environmentRows,
  kindSegment,
} from "@/lib/config-values";
import { formatAgo } from "@/lib/format";

function errorMessage(status: number, body: unknown) {
  const code = typeof body === "object" && body !== null && "error" in body
    ? (body as { error: unknown }).error
    : "";
  const nested = typeof code === "object" && code !== null && "code" in code
    ? String((code as { code: unknown }).code)
    : String(code ?? "");
  if (status === 409 || nested === "kind_conflict") {
    return "That name already exists as the other kind — remove it first to switch between variable and secret";
  }
  if (status === 503 || nested === "not_ready") return "Secret storage is unavailable on the platform right now";
  if (status === 400) return "The platform rejected that name or value";
  return "The platform could not complete that request";
}

function sortedByName(items: ConfigValue[]) {
  return [...items].sort((a, b) => a.name.localeCompare(b.name));
}

const statusLabels: Record<EnvironmentStatus, { label: string; tone: string; title: string }> = {
  set: { label: "Set", tone: "sub", title: "A value is stored" },
  default: { label: "Default", tone: "dim", title: "No value is stored; the default declared in code applies" },
  missing: { label: "Missing", tone: "bad", title: "Required by the live build — deploys fail until it is set" },
  unset: { label: "Not set", tone: "faint", title: "Optional and has no default" },
  kind_mismatch: { label: "Wrong kind", tone: "bad", title: "Stored as the other kind — remove it and add it again" },
  undeclared: { label: "Not declared", tone: "warn", title: "The live build does not declare this name" },
};

function nameList(names: string[]) {
  return names.map((name, index) => (
    <span key={name}>{index > 0 && ", "}<code>{name}</code></span>
  ));
}

export function ConfigPanel({
  scope,
  scopeId,
  scopeLabel,
  initialItems,
  declarations,
  unavailable,
}: {
  scope: ConfigScope;
  scopeId: string;
  scopeLabel: string;
  initialItems: ConfigValue[];
  declarations: EnvironmentDeclarations | null;
  unavailable: boolean;
}) {
  const [items, setItems] = useState(() => sortedByName(initialItems));
  const [kind, setKind] = useState<ConfigValueKind>("variable");
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  // Save results render by the form, removal results by the table; errors
  // announce assertively.
  const [formMessage, setFormMessage] = useState<{ kind: "error" | "status"; content: ReactNode } | null>(null);
  const [tableMessage, setTableMessage] = useState<{ kind: "error" | "status"; content: ReactNode } | null>(null);
  const [confirmingName, setConfirmingName] = useState<string | null>(null);

  const definedNames = new Set(items.map((item) => item.name));
  const rows = environmentRows(items, declarations);
  const missing = rows.filter((row) => row.status === "missing").map((row) => row.name);
  const mismatched = rows.filter((row) => row.status === "kind_mismatch");
  const declarationFor = (entryName: string) =>
    declarations && Object.hasOwn(declarations.environment, entryName) ? declarations.environment[entryName] : undefined;
  const formDeclaration = declarationFor(name);
  const apiPath = (entryKind: ConfigValueKind, entryName: string) =>
    `/api/customer/config/${scope}/${scopeId}/${kindSegment(entryKind)}/${entryName}`;

  async function saveValue(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy("save");
    setFormMessage(null);
    try {
      const response = await fetch(apiPath(kind, name), {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ value }),
      });
      const body = await response.json() as unknown;
      if (!response.ok) {
        setFormMessage({ kind: "error", content: errorMessage(response.status, body) });
        return;
      }
      const saved = body as ConfigValue;
      setItems((current) => sortedByName([...current.filter((item) => item.name !== saved.name), saved]));
      setName("");
      setValue("");
      setFormMessage({
        kind: "status",
        content: saved.kind === "secret"
          ? `Secret ${saved.name} stored — its value is write-only and reaches new Sessions`
          : `Variable ${saved.name} saved — it reaches new Sessions`,
      });
    } catch {
      setFormMessage({ kind: "error", content: "The control service is unavailable" });
    } finally {
      setBusy(null);
    }
  }

  async function removeValue(item: ConfigValue) {
    setBusy(item.name);
    setTableMessage(null);
    try {
      const response = await fetch(apiPath(item.kind, item.name), { method: "DELETE" });
      if (response.status !== 204) {
        setTableMessage({ kind: "error", content: errorMessage(response.status, response.headers.get("content-type")?.includes("json") ? await response.json() as unknown : null) });
        return;
      }
      setItems((current) => current.filter(({ name: existing }) => existing !== item.name));
      setConfirmingName(null);
      const declaration = declarationFor(item.name);
      setTableMessage({
        kind: "status",
        content: declaration?.required && !declaration.default
          ? `Removed ${item.name} — the live build requires it, so deploys fail until it is set again`
          : `Removed ${item.name}`,
      });
    } catch {
      setTableMessage({ kind: "error", content: "The control service is unavailable" });
    } finally {
      setBusy(null);
    }
  }

  function startEdit(item: ConfigValue) {
    setKind(item.kind);
    setName(item.name);
    setValue(item.kind === "variable" ? item.value ?? "" : "");
    setFormMessage(item.kind === "secret"
      ? { kind: "status", content: `Enter a new value for secret ${item.name} — the current value cannot be shown` }
      : null);
  }

  // A declared name takes the kind its declaration expects.
  function startSet(entryName: string, entryKind: ConfigValueKind) {
    setKind(entryKind);
    setName(entryName);
    setValue("");
    setFormMessage(null);
  }

  function changeName(next: string) {
    setName(next);
    const declaration = declarationFor(next);
    if (declaration && !definedNames.has(next)) setKind(declaredKind(declaration));
  }

  return (
    <section aria-label={`Environment for this ${scopeLabel}`}>
      {unavailable && <div className="notice error">Environment is temporarily unavailable</div>}
      {tableMessage && (
        <div className={`notice ${tableMessage.kind}`} role={tableMessage.kind === "error" ? "alert" : "status"}>
          {tableMessage.content}
        </div>
      )}

      {!unavailable && missing.length > 0 && (
        <div className="notice" role="alert">
          Required by the live build and not set: {nameList(missing)} — deploys fail until {missing.length === 1 ? "it is" : "they are"} set
        </div>
      )}
      {!unavailable && mismatched.length > 0 && (
        <div className="notice error" role="alert">
          Stored as the wrong kind: {nameList(mismatched.map((row) => row.name))} — remove {mismatched.length === 1 ? "it" : "them"} and add {mismatched.length === 1 ? "it" : "them"} again as declared
        </div>
      )}

      {!unavailable && (
        <>
          {rows.length === 0 ? (
            <p className="empty-note">No environment values</p>
          ) : (
            <table className="data-table">
              <colgroup>
                <col className="c-id" />
                <col className="c-kind" />
                <col />
                <col className="c-state" />
                <col className="c-time" />
                <col className="c-actions" />
              </colgroup>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Kind</th>
                  <th>Value</th>
                  <th>Status</th>
                  <th className="num">Updated</th>
                  <th aria-label="Actions" />
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => {
                  const item = row.value;
                  const kindShown = item?.kind ?? row.declaredKind ?? "variable";
                  const status = statusLabels[row.status];
                  return (
                    <tr key={row.name}>
                      <td className="ellip">{row.name}</td>
                      <td
                        className={`kind ${kindShown === "secret" ? "warn" : "dim"}`}
                        title={row.status === "kind_mismatch" && row.declaredKind ? `Declared as a ${row.declaredKind}` : undefined}
                      >
                        {kindShown}
                      </td>
                      <td className={`ellip ${item?.kind === "secret" ? "faint" : "sub"}`}>
                        {item
                          ? item.kind === "secret"
                            ? <span title="Secret values are write-only">••••••••</span>
                            : item.value ?? ""
                          : row.declaration?.default
                            ? <span className="faint" title="Default declared in code">{row.declaration.default}</span>
                            : <span className="faint">—</span>}
                      </td>
                      <td className={`ellip ${status.tone}`} title={status.title}>
                        {status.label}{row.declaration?.required && <span className="faint"> · required</span>}
                      </td>
                      <td className="num dim">{item ? formatAgo(item.updated_at) : "—"}</td>
                      <td>
                        <div className="row-actions">
                          {!item ? (
                            <button type="button" onClick={() => startSet(row.name, row.declaredKind ?? "variable")}>
                              Set
                            </button>
                          ) : confirmingName === item.name ? (
                            <>
                              <button type="button" className="confirm-danger" disabled={busy === item.name} onClick={() => void removeValue(item)}>
                                {busy === item.name ? "Removing…" : "Confirm"}
                              </button>
                              <button type="button" disabled={busy === item.name} onClick={() => setConfirmingName(null)}>
                                Cancel
                              </button>
                            </>
                          ) : (
                            <>
                              <button type="button" disabled={busy === item.name} onClick={() => startEdit(item)}>
                                {item.kind === "secret" ? "Replace" : "Edit"}
                              </button>
                              <button type="button" className="danger" disabled={busy === item.name} onClick={() => setConfirmingName(item.name)}>
                                Remove
                              </button>
                            </>
                          )}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}

          <h2 className="section-label" id="new-value">{definedNames.has(name) ? "Update value" : "Add value"}</h2>
          {formMessage && (
            <div className={`notice ${formMessage.kind}`} role={formMessage.kind === "error" ? "alert" : "status"}>
              {formMessage.content}
            </div>
          )}
          {formDeclaration && declaredKind(formDeclaration) !== kind && (
            <div className="notice" role="status">
              The live build declares {name} as a {declaredKind(formDeclaration)} — deploys fail while it is stored as a {kind}
            </div>
          )}
          <form className="inline-form" onSubmit={saveValue}>
            <label>Kind
              <select value={kind} onChange={(event) => setKind(event.target.value as ConfigValueKind)}>
                <option value="variable">Variable</option>
                <option value="secret">Secret (write-only)</option>
              </select>
            </label>
            <label>Name
              <input
                required
                pattern="[A-Za-z_][A-Za-z0-9_]{0,127}"
                maxLength={128}
                value={name}
                onChange={(event) => changeName(event.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "_"))}
                placeholder="DATABASE_URL"
              />
            </label>
            <label className="grow">Value
              <input
                type={kind === "secret" ? "password" : "text"}
                autoComplete="off"
                value={value}
                onChange={(event) => setValue(event.target.value)}
                placeholder={kind === "secret" ? "Stored encrypted, never shown again" : "Plain value"}
              />
            </label>
            <button type="submit" disabled={busy === "save" || !configNamePattern.test(name)}>
              {busy === "save" ? "Saving…" : definedNames.has(name) ? "Update" : "Add"}
            </button>
          </form>
        </>
      )}
    </section>
  );
}
