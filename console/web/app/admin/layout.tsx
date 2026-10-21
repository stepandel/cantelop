import type { ReactNode } from "react";

import { loadHostSnapshot, requireAdminSession } from "@/app/admin/section-data";
import { AdminScreen } from "@/components/admin-screen";
import { viewerOf } from "@/lib/viewer";

export const metadata = { title: "Host telemetry" };

// The frame around every operator screen: one place resolves the host
// snapshot for the sidebar's section counts; the pages below mount the
// telemetry engine with the view they show.
export default async function AdminLayout({ children }: { children: ReactNode }) {
  const session = await requireAdminSession();
  const { snapshot } = await loadHostSnapshot();
  return (
    <AdminScreen viewer={viewerOf(session)} snapshot={snapshot}>
      {children}
    </AdminScreen>
  );
}
