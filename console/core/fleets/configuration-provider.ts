import type { APIConfigurationProvider, ConfigurationProjection } from "../configuration/provider";
import type { EdgeGateProvider } from "../release-activation/edge-gate-provider";

/** Package configuration belongs to its App gate, never to shared code. */
export class FleetConfigurationProvider implements APIConfigurationProvider {
  constructor(private readonly legacy: APIConfigurationProvider, private readonly edge: Pick<EdgeGateProvider, "configure">) {}

  async replaceConfiguration(projection: ConfigurationProjection): Promise<void> {
    if (!projection.scriptName?.startsWith("pkg_")) return this.legacy.replaceConfiguration(projection);
    if (!/^pkg_[0-9a-f]{32}$/.test(projection.scriptName)) throw new TypeError("invalid package identity");
    const entries = [...projection.values, ...(projection.systemBindings ?? [])];
    if (new Set(entries.map(value => value.name)).size !== entries.length) throw new TypeError("duplicate binding");
    const result = await this.edge.configure(projection.appID, projection.revision,
      Object.fromEntries(entries.map(value => [value.name, value.value])));
    if (result.configuration_revision !== projection.revision) throw new Error("Fleet configuration readback mismatch");
  }
}
