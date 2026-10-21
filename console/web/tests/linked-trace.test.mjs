import { expect, test } from "bun:test";

import { includeLinkedRequest } from "../lib/linked-trace";

const request = (id) => ({ id, received_at: "2026-10-14T00:00:00.000Z", logs: [], messages: [] });

test("a linked request stays visible beyond the recent trace limit without duplication", () => {
  const recent = { app_id: "app_1", requests: [request("req_new")], unlinked_messages: [] };
  const linked = { app_id: "app_1", requests: [request("req_old")], unlinked_messages: [] };
  expect(includeLinkedRequest(recent, linked).requests.map(({ id }) => id)).toEqual(["req_old", "req_new"]);
  expect(includeLinkedRequest(recent, { ...linked, requests: [request("req_new")] }).requests.map(({ id }) => id)).toEqual(["req_new"]);
  expect(includeLinkedRequest(recent, { ...linked, requests: [] })).toBe(recent);
});
