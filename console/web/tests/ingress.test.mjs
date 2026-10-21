import { expect, test } from "bun:test";

import { appCommandTypes } from "../../core/api-deployments/commands.ts";
import { commandInfo, commandOrder, consoleSendable, ingressRole } from "../lib/ingress.ts";

const definition = {
  app_name: "agent", session_runtime_id: "rt", webhooks: [{ method: "POST", path: "/webhooks/github" }],
  homepage: true, native_auth: false, token_rotation: true, capabilities: null,
};

test("an SDK 1 App's generated ingress is labelled by what it is for", () => {
  expect(ingressRole(definition, { method: "POST", path: "/commands/dispatch" })).toBe("command");
  expect(ingressRole(definition, { method: "POST", path: "/commands/workspace.resolve" })).toBe("command");
  expect(ingressRole(definition, { method: "GET", path: "/" })).toBe("homepage");
  expect(ingressRole(definition, { method: "HEAD", path: "/" })).toBe("homepage");
  expect(ingressRole(definition, { method: "POST", path: "/webhooks/github" })).toBe("webhook");
  expect(ingressRole(definition, { method: "GET", path: "/webhooks/github" })).toBe("route");
  expect(ingressRole({ ...definition, homepage: false }, { method: "GET", path: "/" })).toBe("route");
});

test("the single /commands path of earlier SDK builds, or an unknown command, is a plain route", () => {
  for (const route of [
    { method: "POST", path: "/commands" },
    { method: "POST", path: "/commands/unknown" },
    { method: "GET", path: "/commands/dispatch" },
  ]) {
    expect(ingressRole(definition, route)).toBe("route");
    expect(commandInfo(route)).toBeUndefined();
  }
});

test("without a definition every path is a plain route, and only commands are not sent from the console", () => {
  expect(ingressRole(undefined, { method: "POST", path: "/commands/dispatch" })).toBe("route");
  expect(consoleSendable("command")).toBe(false);
  for (const role of ["homepage", "webhook", "route"]) expect(consoleSendable(role)).toBe(true);
});

test("every command the platform serves names the SDK call that sends it", () => {
  const infos = appCommandTypes.map((type) => commandInfo({ method: "POST", path: `/commands/${type}` }));
  expect(infos.every((info) => info !== undefined && info.call.endsWith("()"))).toBe(true);
  expect(commandInfo({ method: "POST", path: "/commands/dispatch" })).toMatchObject({ call: "session.dispatch()", group: "messages", work: true });
  expect(commandInfo({ method: "POST", path: "/commands/view" })).toMatchObject({ call: "session.view()", group: "session", work: false });
  // Listed in the order the page shows them: messages first, then reads, then the Workspace.
  const ordered = [...infos].sort((a, b) => commandOrder(a) - commandOrder(b)).map(({ type }) => type);
  expect(ordered.slice(0, 2)).toEqual(["dispatch", "steer"]);
  expect(ordered.at(-1)).toBe("auth");
});
