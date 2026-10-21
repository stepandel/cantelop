import { expect, test } from "bun:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

import { AdminCountStats } from "../components/admin-counts.tsx";

test("renders formatted platform totals with their scope and freshness", () => {
  const html = renderToStaticMarkup(createElement(AdminCountStats, { counts: { users: 1234, apps: 5678 } }));
  expect(html).toContain("<span>Users</span><strong>1,234</strong>");
  expect(html).toContain("<span>Apps</span><strong>5,678</strong>");
  expect(html).toContain("including suspended accounts");
  expect(html).toContain("Non-archived apps, including provisioning apps");
  expect(html).toContain("at page load");
});

test("renders zero as a real count", () => {
  const html = renderToStaticMarkup(createElement(AdminCountStats, { counts: { users: 0, apps: 0 } }));
  expect(html.match(/<strong class="dim">0<\/strong>/g)).toHaveLength(2);
  expect(html).not.toContain("—");
});

test("renders unknown totals as dashes rather than zero", () => {
  const html = renderToStaticMarkup(createElement(AdminCountStats, { counts: null }));
  expect(html.match(/<strong class="dim">—<\/strong>/g)).toHaveLength(2);
});
