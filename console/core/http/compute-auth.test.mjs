import { afterEach, expect, test } from "bun:test";

import { authenticateGatewayProductControl } from "./compute-auth";

const previous = process.env.CANTELOP_GATEWAY_PRODUCT_CONTROL_TOKEN;
const previousCompute = process.env.CANTELOP_COMPUTE_CONTROL_TOKEN;

afterEach(() => {
  if (previous === undefined) delete process.env.CANTELOP_GATEWAY_PRODUCT_CONTROL_TOKEN;
  else process.env.CANTELOP_GATEWAY_PRODUCT_CONTROL_TOKEN = previous;
  if (previousCompute === undefined) delete process.env.CANTELOP_COMPUTE_CONTROL_TOKEN;
  else process.env.CANTELOP_COMPUTE_CONTROL_TOKEN = previousCompute;
});

test("accepts only the configured compute-control bearer credential", () => {
  const token = "a".repeat(64);
  process.env.CANTELOP_GATEWAY_PRODUCT_CONTROL_TOKEN = token;
  process.env.CANTELOP_COMPUTE_CONTROL_TOKEN = "c".repeat(64);
  expect(() => authenticateGatewayProductControl(`Bearer ${token}`)).not.toThrow();
  expect(() => authenticateGatewayProductControl(`Bearer ${"b".repeat(64)}`)).toThrow();
  expect(() => authenticateGatewayProductControl(null)).toThrow();
  process.env.CANTELOP_COMPUTE_CONTROL_TOKEN = token;
  expect(() => authenticateGatewayProductControl(`Bearer ${token}`)).toThrow();
});
