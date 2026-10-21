export type ControlErrorCode = "not_found" | "conflict" | "quota_exceeded" | "user_suspended" |
  "integration_not_configured" | "integration_rotation_unsupported" | "integration_rotation_unresolved" |
  "release_unsupported" | "target_build_missing";

export class ControlError extends Error {
  constructor(readonly code: ControlErrorCode) {
    super(code);
    this.name = "ControlError";
  }
}
