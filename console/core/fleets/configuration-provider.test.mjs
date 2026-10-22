import { expect, test } from "bun:test";
import { FleetConfigurationProvider } from "./configuration-provider";

test("shared configuration projects only to its App gate; standalone releases keep their provider", async () => {
  const calls = [];
  const provider = new FleetConfigurationProvider({ async replaceConfiguration(value) { calls.push(["legacy", value]); } }, {
    async configure(app, revision, bindings) { calls.push(["gate", app, revision, bindings]); return { configuration_revision: revision }; },
  });
  const projection = { appID: `app_${"a".repeat(32)}`, scriptName: `pkg_${"b".repeat(32)}`, revision: 3,
    values: [{ name: "CUSTOMER_SECRET", kind: "secret", value: "current-secret" }],
    systemBindings: [{ name: "CANTELOP_INTEGRATION_TOKEN", kind: "secret", value: "current-token" }] };
  await provider.replaceConfiguration(projection);
  expect(calls).toEqual([["gate", projection.appID, 3, { CUSTOMER_SECRET: "current-secret", CANTELOP_INTEGRATION_TOKEN: "current-token" }]]);
  await provider.replaceConfiguration({ ...projection, scriptName: `rel_${"c".repeat(32)}` });
  expect(calls[1][0]).toBe("legacy");
  await expect(provider.replaceConfiguration({ ...projection, values: [...projection.values, ...projection.values] })).rejects.toThrow("duplicate binding");
});

test("a shared configuration write requires an acknowledged matching revision", async () => {
  const provider = new FleetConfigurationProvider({ async replaceConfiguration() { throw new Error("unexpected code mutation"); } }, {
    async configure() { return { configuration_revision: 1 }; },
  });
  await expect(provider.replaceConfiguration({ appID: `app_${"a".repeat(32)}`, scriptName: `pkg_${"b".repeat(32)}`,
    revision: 2, values: [] })).rejects.toThrow("readback mismatch");
});
