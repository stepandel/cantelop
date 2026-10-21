import type { ReactNode } from "react";
import { Prose } from "@/components/markdown";
import { PostHeader } from "../post-meta";

/* A post uses a slightly wider editorial measure and a shared masthead; its
   body stays plain MDX while title, date, author, and navigation come from the
   index. */
export default function PostLayout({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <Prose measure="wide">
      <PostHeader />
      {children}
    </Prose>
  );
}
