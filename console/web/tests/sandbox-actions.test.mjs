import { expect, test } from "bun:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { SandboxTable } from "../components/instance-tables.tsx";

const sandbox = {
  id: "sbx-0123456789abcdef0123456789abcdef", state: "running",
  workspace_id: "wsp_test", app_id: "app_test", profile: "small", build_id: "build_test",
  vcpus: 1, memory_mib: 256, created_at: "2026-09-16T12:00:00Z", updated_at: "2026-09-16T12:00:00Z",
};
function render(state, admin) {
  return renderToStaticMarkup(createElement(SandboxTable, {
    sandboxes: [{ ...sandbox, state }], observedAt: sandbox.updated_at, cpuRates: {}, history: {},
    polling: false, selectedId: null, onSelect: () => {}, ...(admin ? { onStopped: () => {} } : {}),
  }));
}
test("shared customer table has no stop actions", () => {
  expect(render("running", false)).not.toContain(">Stop</button>");
  expect(render("running", false)).not.toContain(">Actions</th>");
});
test("admin table offers stop for live or recoverable sandboxes, but not terminated ones", () => {
  for (const state of ["starting", "running", "stopping", "failed"]) {
    expect(render(state, true)).toContain(`aria-label="Stop ${sandbox.id}"`);
  }
  expect(render("terminated", true)).not.toContain(">Stop</button>");
});

test("admin table offers crash only for a running sandbox", () => {
  expect(render("running", true)).toContain(`aria-label="Crash ${sandbox.id}"`);
  for (const state of ["starting", "stopping", "failed", "terminated"]) {
    expect(render(state, true)).not.toContain(`aria-label="Crash ${sandbox.id}"`);
  }
  expect(render("running", false)).not.toContain(">Crash</button>");
});

test("legacy sandbox uptime is unknown instead of its cleanup age", () => {
  const html = render("terminated", false);
  expect(html).toContain(">Uptime</th>");
  expect(html).toContain('title="Runtime timing unknown">—</td>');
});

test("usage columns appear only while some sandbox is running", () => {
  expect(render("running", false)).toContain(">CPU</th>");
  expect(render("terminated", false)).not.toContain(">CPU</th>");
  expect(render("terminated", false)).not.toContain(">Memory</th>");
});
