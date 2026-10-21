import { expect, test } from "bun:test";

import { formatVCPU } from "../lib/format.ts";

test("vcpu consumption keeps precision at small readings and drops trailing zeros", () => {
  expect(formatVCPU(0)).toBe("0 vcpu");
  expect(formatVCPU(0.044)).toBe("0.044 vcpu");
  expect(formatVCPU(0.34)).toBe("0.34 vcpu");
  expect(formatVCPU(2)).toBe("2 vcpu");
  expect(formatVCPU(12.25)).toBe("12.3 vcpu");
});
