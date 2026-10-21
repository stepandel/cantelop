import type { AdminCounts } from "@core/db/admin-counts";

export function AdminCountStats({ counts }: { counts: AdminCounts | null }) {
  return (
    <>
      <div className="stat" title="All registered users, including suspended accounts · at page load">
        <span>Users</span>
        <strong className={counts === null || counts.users === 0 ? "dim" : undefined}>
          {counts?.users.toLocaleString("en-US") ?? "—"}
        </strong>
      </div>
      <div className="stat" title="Non-archived apps, including provisioning apps · at page load">
        <span>Apps</span>
        <strong className={counts === null || counts.apps === 0 ? "dim" : undefined}>
          {counts?.apps.toLocaleString("en-US") ?? "—"}
        </strong>
      </div>
    </>
  );
}
