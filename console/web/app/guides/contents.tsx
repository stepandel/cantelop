"use client";

import { useEffect, useState } from "react";
import styles from "./guides.module.css";

/* A guide's table of contents, marking the section the reader is in. The
   current section is the last heading to have passed under the sticky bar;
   before the first one, nothing is marked. */
export function Contents({ sections }: { sections: { id: string; title: string }[] }) {
  const [current, setCurrent] = useState<string | null>(null);

  useEffect(() => {
    const ids = sections.map(({ id }) => id);
    const update = () => {
      const line = 120;
      let active: string | null = null;
      for (const id of ids) {
        const element = document.getElementById(id);
        if (element && element.getBoundingClientRect().top <= line) active = id;
      }
      setCurrent(active);
    };
    update();
    window.addEventListener("scroll", update, { passive: true });
    window.addEventListener("resize", update);
    return () => {
      window.removeEventListener("scroll", update);
      window.removeEventListener("resize", update);
    };
  }, [sections]);

  return (
    <nav aria-label="In this guide">
      <ol>
        {sections.map(({ id, title }) => (
          <li key={id}>
            <a
              href={`#${id}`}
              className={id === current ? styles.current : undefined}
              aria-current={id === current ? "location" : undefined}
            >
              {title}
            </a>
          </li>
        ))}
      </ol>
    </nav>
  );
}
