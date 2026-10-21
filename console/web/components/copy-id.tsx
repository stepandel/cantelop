"use client";

import { useCopy } from "@/lib/use-copy";

// Identifier with a copy affordance. Resource ids (app_…, wsp_…) remain useful
// for API and operator commands even though cantelop.json targets Apps by slug.
export function CopyId({ value }: { value: string }) {
  const { copied, copy } = useCopy();

  return (
    <button type="button" className="copy-id" onClick={() => void copy(value)} aria-label={`Copy ${value}`}>
      {value}
      <i aria-hidden="true">{copied ? "Copied" : "Copy"}</i>
      <span className="visually-hidden" role="status">{copied ? "Copied to clipboard" : ""}</span>
    </button>
  );
}
