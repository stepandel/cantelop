import { timingSafeEqual } from "node:crypto";

export function authenticateGatewayProductControl(authorization: string | null): void {
  const configured = process.env.CANTELOP_GATEWAY_PRODUCT_CONTROL_TOKEN?.trim();
  const compute = process.env.CANTELOP_COMPUTE_CONTROL_TOKEN?.trim();
  if (!configured || !/^[0-9a-f]{64}$/.test(configured) ||
      configured === compute ||
      !authorization?.startsWith("Bearer ") || authorization.includes(",")) {
    throw new Error("invalid gateway product-control credential");
  }
  const presented = authorization.slice("Bearer ".length);
  if (presented.length !== configured.length ||
      !timingSafeEqual(Buffer.from(presented), Buffer.from(configured))) {
    throw new Error("invalid gateway product-control credential");
  }
}
