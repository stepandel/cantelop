import { expect, test } from "bun:test";

import { apiArtifactRoutes, appCommandType, isSDKOneEdgeManifest, parseAPIArtifactManifest, validAPIArtifactManifest } from "./artifact";
import { edge } from "./protocol-artifact.test.mjs";

test("rejects every pre-1.0 customer-authored API manifest", () => {
  for (const legacy of [
    { schema_version: 2, kind: "cantelop-edge-api", main_module: "worker.mjs" },
    { schema_version: 3, kind: "cantelop-edge-api", main_module: "worker.mjs", routes: [] },
    { schema_version: 3, kind: "cantelop-edge-api", main_module: "worker.mjs", routes: null },
  ]) {
    expect(isSDKOneEdgeManifest(legacy)).toBe(false);
    expect(() => parseAPIArtifactManifest(legacy)).toThrow("invalid API artifact manifest");
    expect(validAPIArtifactManifest(legacy)).toBe(false);
  }
  expect(() => parseAPIArtifactManifest(null)).toThrow("invalid API artifact manifest");
});

test("accepts the generated Edge manifest and derives its ingress", () => {
  expect(isSDKOneEdgeManifest(edge)).toBe(true);
  expect(validAPIArtifactManifest(parseAPIArtifactManifest(edge))).toBe(true);
  expect(apiArtifactRoutes(parseAPIArtifactManifest(edge))).toEqual([
    { method: "GET", path: "/" },
    { method: "HEAD", path: "/" },
    { method: "POST", path: "/commands/app.environment.set" },
    { method: "POST", path: "/commands/app.environment.status" },
    { method: "POST", path: "/commands/app.environment.unset" },
    { method: "POST", path: "/commands/cancel" },
    { method: "POST", path: "/commands/dispatch" },
    { method: "POST", path: "/commands/request" },
    { method: "POST", path: "/commands/status" },
    { method: "POST", path: "/commands/steer" },
    { method: "POST", path: "/commands/stop" },
    { method: "POST", path: "/commands/stream" },
    { method: "POST", path: "/commands/view" },
    { method: "POST", path: "/commands/workspace.database" },
    { method: "POST", path: "/commands/workspace.delete" },
    { method: "POST", path: "/commands/workspace.inspect" },
    { method: "POST", path: "/commands/workspace.resolve" },
    { method: "POST", path: "/webhooks/github" },
  ]);
  // Auth commands are served only when the App declares auth.
  expect(apiArtifactRoutes(parseAPIArtifactManifest({ ...edge, auth: true }))).toContainEqual({ method: "POST", path: "/commands/auth" });
});

test("names the command a path addresses", () => {
  expect(appCommandType("/commands/dispatch")).toBe("dispatch");
  expect(appCommandType("/commands/workspace.resolve")).toBe("workspace.resolve");
  for (const path of ["/commands", "/commands/", "/commands/unknown", "/webhooks/dispatch", "/commands/dispatch/x"]) {
    expect(appCommandType(path)).toBeUndefined();
  }
});
