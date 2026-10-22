import { expect, test } from "bun:test";

import { installationStatus } from "./repository";

const ready = { desired_state: "ready", release_id: "rel_a", release_status: "active", active_release_id: "rel_a" };

test("derives each installation status from its desired state and release", () => {
  expect(installationStatus(ready)).toBe("ready");
  expect(installationStatus({ ...ready, active_release_id: "rel_b" })).toBe("provisioning");
  expect(installationStatus({ desired_state: "ready", release_id: null, release_status: null })).toBe("provisioning");
  expect(installationStatus({ ...ready, next_package_id: "pkg_b" })).toBe("upgrading");
  expect(installationStatus({ ...ready, error_code: "release_failed" })).toBe("failed");
  expect(installationStatus({ ...ready, release_status: "failed" })).toBe("failed");
  expect(installationStatus({ ...ready, release_status: "cancelled" })).toBe("failed");
  expect(installationStatus({ ...ready, desired_state: "suspended", next_package_id: "pkg_b" })).toBe("suspended");
  expect(installationStatus({ desired_state: "deleted", app_status: "active", purged: true })).toBe("deleting");
  expect(installationStatus({ desired_state: "deleted", app_status: "archived", purged: false })).toBe("deleting");
  expect(installationStatus({ desired_state: "deleted", app_status: "archived", purged: true })).toBe("deleted");
});
