import type { CreateAppInput, CreateWorkspaceInput } from "../domain/model";
import { platformInternalPort, validateCreateApp, validateCreateWorkspace } from "../domain/model";

export function publicCreateAppInput(input: unknown): CreateAppInput {
  const root = record(input, ["slug", "session_runtime"]);
  const sessionRuntime = record(root.session_runtime, ["runtime"]);
  const runtime = record(sessionRuntime.runtime, ["profile"]);
  if (typeof root.slug !== "string" || runtime.profile !== "default") {
    throw new TypeError("invalid App input");
  }
  return validateCreateApp({
    slug: root.slug,
    sessionRuntime: {
      internalPort: platformInternalPort,
    },
  });
}

export function publicCreateWorkspaceInput(input: unknown): CreateWorkspaceInput {
  const root = record(input, ["app_id", "slug"]);
  if (typeof root.app_id !== "string" || typeof root.slug !== "string") throw new TypeError("invalid Workspace input");
  return validateCreateWorkspace({ appID: root.app_id, slug: root.slug });
}

function record(input: unknown, keys: string[]): Record<string, unknown> {
  if (typeof input !== "object" || input === null || Array.isArray(input)) throw new TypeError("invalid input");
  const value = input as Record<string, unknown>;
  const actual = Object.keys(value).sort();
  const expected = [...keys].sort();
  if (actual.length !== expected.length || actual.some((key, index) => key !== expected[index])) throw new TypeError("invalid input");
  return value;
}
