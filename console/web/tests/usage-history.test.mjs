import { expect, test } from "bun:test";
import { appendUsageSamples } from "../lib/usage-history.ts";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { SandboxTable } from "../components/instance-tables.tsx";
import { SandboxMonitor } from "../components/sandbox-monitor.tsx";

function sandbox(used, at = "2026-09-30T12:00:00Z") {
  return {
    id: "sbx_test", state: "running", workspace_id: "wsp_test", app_id: "app_test",
    profile: "default", build_id: "build_test", vcpus: 4, memory_mib: 512,
    memory_max_mib: 4608, created_at: at, updated_at: at,
    usage: { observed_at: at, cpu_usage_us: 1234, memory_current_bytes: 3 * 1024 ** 3,
      guest_memory_mib: 4096, ...(used === undefined ? {} : { guest_memory_used_bytes: used }) },
  };
}
function snapshot(item) { return { sandboxes: [item] }; }
function render(item) {
  return renderToStaticMarkup(createElement(SandboxTable, {
    sandboxes: [item], observedAt: item.updated_at, cpuRates: {}, history: {},
    polling: true, selectedId: null, onSelect: () => {},
  }));
}

test("guest consumption falls while host consumption and guest allocation remain high", () => {
  const busy = sandbox(3 * 1024 ** 3);
  const idle = sandbox(100 * 1024 ** 2, "2026-09-30T12:00:05Z");
  const history = appendUsageSamples(appendUsageSamples({}, snapshot(busy), {}), snapshot(idle), {});
  expect(history.sbx_test.map(sample => sample.memory_bytes)).toEqual([3 * 1024 ** 3, 100 * 1024 ** 2]);
  const html = render(idle);
  expect(html).toContain("100 mib");
  expect(html).not.toContain("3.00 gib");
  const monitor = renderToStaticMarkup(createElement(SandboxMonitor, {
    instance: idle, samples: history.sbx_test, live: true, onClose: () => {},
  }));
  expect(monitor).toContain('class="chart-current">100 mib');
});

test("missing guest telemetry stays unavailable rather than using host RAM", () => {
  const item = sandbox(undefined);
  expect(appendUsageSamples({}, snapshot(item), {}).sbx_test[0].memory_bytes).toBeUndefined();
  expect(render(item)).not.toContain("3.00 gib");
});

test("zero guest consumption is retained as a real sample", () => {
  const item = sandbox(0);
  expect(appendUsageSamples({}, snapshot(item), {}).sbx_test[0].memory_bytes).toBe(0);
});

test("missing current guest telemetry does not display an old reading as current", () => {
  const item = sandbox(undefined);
  const monitor = renderToStaticMarkup(createElement(SandboxMonitor, {
    instance: item, live: true, onClose: () => {}, samples: [
      { observed_at: "2026-09-30T12:00:00Z", memory_bytes: 3 * 1024 ** 3 },
      { observed_at: "2026-09-30T12:00:05Z" },
    ],
  }));
  expect(monitor).toContain('class="chart-current">Unavailable');
});
