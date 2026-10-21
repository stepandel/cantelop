import type { AppDeploymentConfiguration } from "../api-deployments/protocol-artifact";
import type { ConfigProjectionValue } from "./repository";
export class AppEnvironmentError extends TypeError {
  constructor() {
    super("app_environment_missing_or_invalid");
  }
}
/** Validate declared bindings without exposing their values in errors. */
export function validateAppEnvironment(
  configuration: AppDeploymentConfiguration,
  values: readonly ConfigProjectionValue[],
): void {
  const actual = new Map(values.map((value) => [value.name, value]));
  for (const [name, declaration] of Object.entries(
    configuration.environment ?? {},
  )) {
    const configured = actual.get(name);
    if (
      configured &&
      configured.kind !== (declaration.secret ? "secret" : "variable")
    )
      throw new AppEnvironmentError();
    if (
      declaration.required &&
      (configured?.value ?? declaration.default ?? "") === ""
    )
      throw new AppEnvironmentError();
  }
}
