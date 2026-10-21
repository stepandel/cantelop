"use client";

import { usePathname } from "next/navigation";

import { useCopy } from "@/lib/use-copy";

import styles from "./prose.module.css";

/* Hands the reader the page as markdown, for pasting into a model that would
   otherwise be given a rendering of it. The sources are read where the section
   is laid out, so this stays one control any markdown section can offer. */
export function CopyPage({
  sources,
  placement = "title",
}: {
  sources: Record<string, string>;
  placement?: "title" | "above-title";
}) {
  const pathname = usePathname();
  const { copied, copy: copyToClipboard } = useCopy();

  const source = sources[pathname];
  if (!source) {
    return null;
  }

  const copy = () => copyToClipboard(source);

  return (
    <button
      type="button"
      className={`${styles.copyPage} ${placement === "above-title" ? styles.copyPageAbove : ""}`}
      onClick={copy}
      aria-label="Copy this page as markdown"
    >
      {copied ? "Copied" : "Copy for LLM"}
      <span className="visually-hidden" role="status">
        {copied ? "Copied to clipboard" : ""}
      </span>
    </button>
  );
}
