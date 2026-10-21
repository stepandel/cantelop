import type { ReactNode } from "react";

/** The header of one App section page: what this page is, and at most two
 *  numbers that summarize it. The App's identity lives in the sidebar and the
 *  breadcrumb — only the workspaces landing page repeats the URL and id. */
export function SectionHead({
  title,
  subtitle,
  children,
}: {
  title: string;
  subtitle?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="screen-head">
      <div className="head-id">
        <h1>{title}</h1>
        {subtitle && <span>{subtitle}</span>}
      </div>
      {children}
    </div>
  );
}

export function HeadStat({
  label,
  value,
  tone,
  title,
}: {
  label: string;
  value: ReactNode;
  tone?: "ok" | "info" | "warn" | "bad" | "dim" | "quiet";
  title?: string;
}) {
  return (
    <div className="stat">
      <span>{label}</span>
      <strong className={tone} title={title}>{value}</strong>
    </div>
  );
}
