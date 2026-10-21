"use client";

import { useEffect, useRef, useState, type KeyboardEvent } from "react";

import { useCopy } from "@/lib/use-copy";

import { USE_CASES, starterPrompt, type UseCase } from "@/lib/use-cases";

/* The use-case picker's state, shared by the front door and the console's
   example dialog so the two tab strips behave identically: one selected job
   out of the list given, arrow keys walk the strip, and copy reports itself
   for 1.6s. The prompt's docs link takes the page's own origin once the
   browser is known, so a server pass renders it blank rather than guessing
   a host. Every job's prompt is returned, not only the chosen one, so a
   surface can stack them and hold one height across the strip. */
export function useUseCasePicker(useCases: readonly UseCase[] = USE_CASES) {
  const [active, setActive] = useState(0);
  const [origin, setOrigin] = useState("");
  const { copied, copy: copyToClipboard, reset: resetCopied } = useCopy();
  const tabs = useRef<Array<HTMLButtonElement | null>>([]);

  useEffect(() => {
    setOrigin(window.location.origin);
  }, []);

  const current = useCases[active];
  const prompts = useCases.map((useCase) => starterPrompt(useCase, origin));
  const text = prompts[active];

  const select = (index: number) => {
    setActive(index);
    resetCopied();
  };

  const onKeyDown = (event: KeyboardEvent<HTMLElement>) => {
    const step = event.key === "ArrowRight" ? 1 : event.key === "ArrowLeft" ? -1 : 0;
    if (step === 0) return;
    event.preventDefault();
    const next = (active + step + useCases.length) % useCases.length;
    select(next);
    tabs.current[next]?.focus();
  };

  const tabRef = (index: number) => (node: HTMLButtonElement | null) => {
    tabs.current[index] = node;
  };

  const copy = () => copyToClipboard(text);

  return { active, current, text, prompts, copied, select, onKeyDown, tabRef, copy };
}
