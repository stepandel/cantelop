import { AsyncLocalStorage } from "node:async_hooks";

// Next normally installs this before loading server modules. Bun's test runner
// imports routes directly; initialize it before Next captures a throwing fallback.
if (typeof Reflect.get(globalThis, "AsyncLocalStorage") !== "function") {
  Reflect.set(globalThis, "AsyncLocalStorage", AsyncLocalStorage);
}
