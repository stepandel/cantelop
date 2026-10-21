// Config values are shared by the Edge Worker and every Session runtime of an
// App. Secrets are write-only — the control plane never returns their value.
// Apps declare their environment in code; the live release's declarations
// say which values the App expects.

export type ConfigScope = "apps";

export type ConfigValueKind = "variable" | "secret";

// Path segment used by the tenant API for each kind.
export type ConfigKindSegment = "variables" | "secrets";

export interface ConfigValue {
  name: string;
  kind: ConfigValueKind;
  value?: string; // present only for variables; absent for secrets
  is_empty?: boolean; // present for secrets without exposing their value or length
  updated_at: string;
}

export interface EnvironmentDeclaration {
  default?: string;
  secret?: boolean;
  required?: boolean;
}

export interface EnvironmentDeclarations {
  release_id: string;
  version: number;
  environment: Record<string, EnvironmentDeclaration>;
}

export interface ConfigValuePage {
  items: ConfigValue[];
  // Null until the App has a live release.
  declarations: EnvironmentDeclarations | null;
}

// How a name stands against the live release's declarations, mirroring the
// deploy-time check: a required value is missing when neither a stored value
// nor a default is non-empty, and a stored value must match the declared kind.
// Before the first deploy nothing is declared yet, so stored values are just
// set; the first deploy checks them.
export type EnvironmentStatus = "set" | "default" | "missing" | "unset" | "kind_mismatch" | "undeclared";

export interface EnvironmentRow {
  name: string;
  value?: ConfigValue;
  declaration?: EnvironmentDeclaration;
  declaredKind?: ConfigValueKind;
  status: EnvironmentStatus;
}

export function declaredKind(declaration: EnvironmentDeclaration): ConfigValueKind {
  return declaration.secret ? "secret" : "variable";
}

// Declared names first, then stored values the release no longer declares.
export function environmentRows(items: ConfigValue[], declarations: EnvironmentDeclarations | null): EnvironmentRow[] {
  if (declarations === null) {
    return [...items]
      .sort((a, b) => a.name.localeCompare(b.name))
      .map((item) => ({ name: item.name, value: item, status: "set" }));
  }
  const stored = new Map(items.map((item) => [item.name, item]));
  const declared = Object.entries(declarations.environment)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, declaration]): EnvironmentRow => {
      const value = stored.get(name);
      const kind = declaredKind(declaration);
      return { name, value, declaration, declaredKind: kind, status: declaredStatus(value, declaration, kind) };
    });
  const undeclared = items
    .filter((item) => !Object.hasOwn(declarations.environment, item.name))
    .sort((a, b) => a.name.localeCompare(b.name))
    .map((item): EnvironmentRow => ({ name: item.name, value: item, status: "undeclared" }));
  return [...declared, ...undeclared];
}

function declaredStatus(value: ConfigValue | undefined, declaration: EnvironmentDeclaration, kind: ConfigValueKind): EnvironmentStatus {
  if (value && value.kind !== kind) return "kind_mismatch";
  if (value) {
    // Secrets expose only emptiness. A stored empty value hides any default,
    // as it does at deploy.
    const empty = value.kind === "secret" ? value.is_empty === true : value.value === "";
    return empty && declaration.required ? "missing" : "set";
  }
  if ((declaration.default ?? "") !== "") return "default";
  return declaration.required ? "missing" : "unset";
}

export const configScopeIDPatterns: Record<ConfigScope, RegExp> = {
  apps: /^app_[0-9a-f]{32}$/,
};

export const configNamePattern = /^[A-Za-z_][A-Za-z0-9_]{0,127}$/;

export const maxConfigValueBytes = 64 * 1024;

export function kindSegment(kind: ConfigValueKind): ConfigKindSegment {
  return kind === "variable" ? "variables" : "secrets";
}

export function isConfigScope(value: string): value is ConfigScope {
  return value === "apps";
}

export function isConfigKindSegment(value: string): value is ConfigKindSegment {
  return value === "variables" || value === "secrets";
}
