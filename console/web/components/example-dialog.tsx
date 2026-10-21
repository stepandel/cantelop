"use client";

import { useEffect, useId, useRef } from "react";

import { UseCaseGlyph } from "@/components/use-case-glyph";
import { USE_CASES } from "@/lib/use-cases";
import { useUseCasePicker } from "@/lib/use-use-case";

/* The front door's use-case picker, spoken in console vocabulary and raised
   over the home screen from its "start from example" link (or the palette's
   command) on #example, the way the create form opens on #new-app. A native
   dialog: modal, Escape and the backdrop close it, focus is trapped and
   handed back to the opener for free. Inside, the chips are the
   observability pages' tab row, the head is a .monitor-head, and the prompt
   sits in a surface block drawn like an input — something to copy out of,
   not read past. It is one step — pick a job, copy, paste into the coding
   agent — so there is no stepper; the coding agent's `cantelop deploy`
   creates the app, and the card that appears is the proof it worked. */
export function ExampleDialog({ onClose }: { onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null);
  const id = useId();
  const { active, current, prompts, copied, select, onKeyDown, tabRef, copy } = useUseCasePicker();

  // Opened on mount. Left alone, the dialog would hand focus to its first
  // control, the close button; the chosen chip is the better start, since
  // the arrows walk the strip from there.
  useEffect(() => {
    const dialog = ref.current;
    if (!dialog || dialog.open) return;
    dialog.showModal();
    dialog.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]')?.focus();
  }, []);

  return (
    <dialog
      ref={ref}
      className="example-dialog"
      aria-labelledby={`${id}-title`}
      // The dialog box itself has no padding, so a click that lands on the
      // element rather than a child is a click on the backdrop. Every way
      // out — Escape, the backdrop, the close control — ends in the native
      // close event, which is the one place the opener hears about it.
      onClick={(event) => {
        if (event.target === event.currentTarget) event.currentTarget.close();
      }}
      onClose={onClose}
    >
      <div className="example-body">
        <div className="example-head">
          <h2 id={`${id}-title`}>Start from an example</h2>
          <button type="button" className="tabrow-action" onClick={() => ref.current?.close()}>Close</button>
        </div>

        <div className="tabrow" onKeyDown={onKeyDown}>
          <div className="tablist" role="tablist" aria-label="Use case">
            {USE_CASES.map((useCase, index) => (
              <button
                key={useCase.slug}
                ref={tabRef(index)}
                type="button"
                role="tab"
                id={`${id}-tab-${useCase.slug}`}
                className="tab-chip"
                aria-selected={index === active}
                aria-controls={`${id}-panel`}
                aria-label={useCase.label}
                title={useCase.label}
                tabIndex={index === active ? 0 : -1}
                onClick={() => select(index)}
              >
                <UseCaseGlyph name={useCase.icon} className="example-glyph" size={14} />
                {useCase.short}
              </button>
            ))}
          </div>
        </div>

        <div
          id={`${id}-panel`}
          role="tabpanel"
          aria-labelledby={`${id}-tab-${current.slug}`}
          className="example-panel"
        >
          <div className="monitor-head">
            <strong>{current.label}</strong>
            <dl className="example-meta">
              <div className="monitor-meta"><dt>App</dt><dd>{current.app}</dd></div>
            </dl>
            <button type="button" className="tabrow-action" onClick={copy}>
              {copied ? "Copied" : "Copy prompt"}
              <span className="visually-hidden" role="status">{copied ? "Copied to clipboard" : ""}</span>
            </button>
          </div>
          {/* Stacked in one grid cell, so the dialog holds one height
              across the strip; only the chosen prompt is shown. */}
          <div className="example-prompts">
            {USE_CASES.map((useCase, index) => (
              <pre
                key={useCase.slug}
                className="example-prompt"
                data-active={index === active}
                aria-hidden={index !== active}
                tabIndex={index === active ? 0 : -1}
              >
                {prompts[index]}
              </pre>
            ))}
          </div>
        </div>
      </div>
    </dialog>
  );
}
