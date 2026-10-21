import type { CustomerApp } from "@/lib/customer-telemetry";

/** The viewer's standing on an App as the App header states it: what they
 *  are, and who else is on it. An owner reads their admin count; an admin
 *  reads whose app it is. */
export function accessLabel(app: Pick<CustomerApp, "role" | "owner" | "admin_count">): string {
  if (app.role !== "owner") return `Admin · owned by ${app.owner?.email ?? "another account"}`;
  if (app.admin_count === 0) return "Owner · no admins";
  return `Owner · ${app.admin_count} admin${app.admin_count === 1 ? "" : "s"}`;
}
