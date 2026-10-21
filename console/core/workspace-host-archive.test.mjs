import { expect, test } from "bun:test";

import { waitForWorkspaceHostArchive } from "./workspace-host-archive";

const workspaceID = "wsp_0123456789abcdef0123456789abcdef";

test("returns once the compute host has archived the Workspace", async () => {
  let checks = 0;
  const sleeps = [];
  const products = { async isWorkspaceHostArchived(id) { expect(id).toBe(workspaceID); return ++checks === 3; } };
  expect(await waitForWorkspaceHostArchive(products, workspaceID, 5_000, 100, async (ms) => { sleeps.push(ms); })).toBe(true);
  expect(sleeps).toEqual([100, 100]);
});

test("reports pending after the timeout", async () => {
  let checks = 0;
  const products = { async isWorkspaceHostArchived() { checks++; return false; } };
  expect(await waitForWorkspaceHostArchive(products, workspaceID, 300, 100, async () => {})).toBe(false);
  expect(checks).toBe(4);
});
