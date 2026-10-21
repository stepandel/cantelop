// <stdin>
import { CantelopClient } from "@cantelop/sdk";
var app = new CantelopClient().app({
  name: "cantelop-claude-api",
  runtime: { receive() {
    throw new Error("App runtime executes in the Sandbox");
  }, [Symbol.for("dev.cantelop.sdk.compiled-runtime.v1")]: "rt_22c8705c073991522fd7fe8c99042ff1af5fb1f90e5895463bb11f09814ddd11" },
  auth: { [Symbol.for("dev.cantelop.sdk.compiled-auth.v1")]: true },
  dockerfile: "node_modules/@cantelop/sdk/dist/harnesses/claude/Dockerfile"
});
export {
  app
};
