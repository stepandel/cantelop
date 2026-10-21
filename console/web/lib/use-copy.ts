"use client";

import { useCallback, useEffect, useRef, useState } from "react";

const COPIED_MS = 1_600;

/**
 * Clipboard copy with a short-lived "copied" flag. When the clipboard is
 * unavailable (permissions, insecure context) nothing claims a copy happened
 * and the text stays selectable.
 */
export function useCopy(): { copied: boolean; copy: (text: string) => Promise<void>; reset: () => void } {
  const [copied, setCopied] = useState(false);
  const timer = useRef<number | undefined>(undefined);

  useEffect(() => () => window.clearTimeout(timer.current), []);

  const copy = useCallback(async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      return;
    }
    setCopied(true);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setCopied(false), COPIED_MS);
  }, []);

  const reset = useCallback(() => {
    window.clearTimeout(timer.current);
    setCopied(false);
  }, []);

  return { copied, copy, reset };
}
