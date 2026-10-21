import { expect, test } from "bun:test";

import { environmentRows } from "./config-values";

const at = "2026-08-12T12:00:00Z";
const declarations = (environment) => ({ release_id: "rel_0123456789abcdef0123456789abcdef", version: 3, environment });
const statuses = (items, environment) =>
  Object.fromEntries(environmentRows(items, declarations(environment)).map((row) => [row.name, row.status]));

test("declared names report whether the deploy-time check would pass", () => {
  expect(statuses([
    { name: "API_KEY", kind: "secret", updated_at: at },
    { name: "LOG_LEVEL", kind: "variable", value: "debug", updated_at: at },
  ], {
    API_KEY: { secret: true, required: true },
    LOG_LEVEL: { default: "info" },
    MODEL: { default: "sonnet", required: true },
    SIGNING_SECRET: { secret: true, required: true },
    TIMEOUT_MS: {},
  })).toEqual({
    API_KEY: "set",
    LOG_LEVEL: "set",
    MODEL: "default",
    SIGNING_SECRET: "missing",
    TIMEOUT_MS: "unset",
  });
});

test("a stored empty variable hides the default of a required name", () => {
  expect(statuses([{ name: "MODEL", kind: "variable", value: "", updated_at: at }], {
    MODEL: { default: "sonnet", required: true },
  })).toEqual({ MODEL: "missing" });
  expect(statuses([{ name: "MODEL", kind: "variable", value: "", updated_at: at }], {
    MODEL: { default: "sonnet" },
  })).toEqual({ MODEL: "set" });
});

test("values of the other kind and undeclared values are flagged", () => {
  const rows = environmentRows([
    { name: "ZETA", kind: "variable", value: "1", updated_at: at },
    { name: "TOKEN", kind: "variable", value: "plain", updated_at: at },
    { name: "ALPHA", kind: "variable", value: "2", updated_at: at },
  ], declarations({ TOKEN: { secret: true } }));
  expect(rows.map(({ name, status, declaredKind }) => ({ name, status, declaredKind }))).toEqual([
    { name: "TOKEN", status: "kind_mismatch", declaredKind: "secret" },
    { name: "ALPHA", status: "undeclared", declaredKind: undefined },
    { name: "ZETA", status: "undeclared", declaredKind: undefined },
  ]);
});

test("before the first deploy stored values are simply set", () => {
  expect(environmentRows([
    { name: "ZETA", kind: "secret", updated_at: at },
    { name: "ALPHA", kind: "variable", value: "1", updated_at: at },
  ], null).map(({ name, status }) => [name, status])).toEqual([["ALPHA", "set"], ["ZETA", "set"]]);
});

test("required empty secrets match deploy validation without exposing their values", () => {
  expect(statuses([
    { name: "EMPTY_REQUIRED", kind: "secret", is_empty: true, updated_at: at },
    { name: "EMPTY_OPTIONAL", kind: "secret", is_empty: true, updated_at: at },
    { name: "FILLED_REQUIRED", kind: "secret", is_empty: false, updated_at: at },
  ], {
    EMPTY_REQUIRED: { secret: true, required: true },
    EMPTY_OPTIONAL: { secret: true },
    FILLED_REQUIRED: { secret: true, required: true },
  })).toEqual({ EMPTY_REQUIRED: "missing", EMPTY_OPTIONAL: "set", FILLED_REQUIRED: "set" });
});
