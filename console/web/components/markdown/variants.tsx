"use client";

import {
  Children,
  cloneElement,
  createContext,
  isValidElement,
  useContext,
  useId,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactElement,
  type ReactNode,
} from "react";

import styles from "./prose.module.css";

/* One instruction with several forms — a language, an OS, a package manager,
   an agent SDK. Tabs let the reader pick once and keep reading their own
   version of the page instead of scanning near-identical blocks.

   Blocks that share a group stay in step, so a choice made in one step still
   holds three steps later. The selection lives in <Prose>, which every
   markdown page already renders into, so a page needs no layout of its own to
   use this. */
type Selection = {
  chosen: Record<string, string>;
  choose: (group: string, variant: string) => void;
};

const VariantContext = createContext<Selection | null>(null);

/* A fence's variant tag is a slug — shell:openai — and the tab shows the
   name behind it. Anything not listed here shows as tagged. */
const LABELS: Record<string, string> = {
  homebrew: "Homebrew",
  curl: "curl",
  openai: "OpenAI",
  claude: "Claude",
  pi: "Pi",
};

export function VariantProvider({ children }: { children: ReactNode }) {
  const [chosen, setChosen] = useState<Record<string, string>>({});
  const choose = (group: string, variant: string) =>
    setChosen((current) => ({ ...current, [group]: variant }));

  return (
    <VariantContext.Provider value={{ chosen, choose }}>
      {children}
    </VariantContext.Provider>
  );
}

/* Each child is a fenced block labelled with its variant — ```shell:openai —
   which the markdown component map turns into a Snippet carrying that label. */
type VariantBlock = ReactElement<{
  variant?: string;
  head?: ReactNode;
  panel?: { id: string; labelledBy: string };
}>;

export function Variants({ group, children }: { group: string; children: ReactNode }) {
  const context = useContext(VariantContext);
  const id = useId();
  const buttons = useRef<Array<HTMLButtonElement | null>>([]);

  const blocks = Children.toArray(children).filter(
    (child): child is VariantBlock =>
      isValidElement(child) && typeof (child.props as { variant?: string }).variant === "string",
  );

  if (!context) {
    throw new Error("<Variants> must render inside <Prose>");
  }
  if (blocks.length === 0) {
    throw new Error(
      `<Variants group="${group}"> has no labelled blocks — tag each fence like \`\`\`shell:name`,
    );
  }

  const names = blocks.map((block) => block.props.variant as string);
  const chosen = names.indexOf(context.chosen[group] ?? "");
  const active = chosen === -1 ? 0 : chosen;

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const step = event.key === "ArrowRight" ? 1 : event.key === "ArrowLeft" ? -1 : 0;
    if (step === 0) {
      return;
    }
    event.preventDefault();
    const next = (active + step + names.length) % names.length;
    context.choose(group, names[next]);
    buttons.current[next]?.focus();
  };

  /* The tab strip stands in the block's own strip, where a lone block would
     show its name, so the choice and the code read as one panel. */
  const tabs = (
    <div
      className={styles.variantTabs}
      role="tablist"
      aria-label={group}
      onKeyDown={onKeyDown}
    >
      {names.map((name, index) => (
        <button
          key={name}
          ref={(node) => {
            buttons.current[index] = node;
          }}
          type="button"
          role="tab"
          id={`${id}-${name}`}
          className={styles.variantTab}
          aria-selected={index === active}
          aria-controls={`${id}-panel`}
          tabIndex={index === active ? 0 : -1}
          onClick={() => context.choose(group, name)}
        >
          {LABELS[name] ?? name}
        </button>
      ))}
    </div>
  );

  return cloneElement(blocks[active], {
    head: tabs,
    panel: { id: `${id}-panel`, labelledBy: `${id}-${names[active]}` },
  });
}
