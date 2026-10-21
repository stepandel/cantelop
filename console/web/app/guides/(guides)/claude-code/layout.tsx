import type { ReactNode } from "react";
import { GuideLayout, guideMetadata } from "../../guide-layout";
import { CLAUDE_CODE_GUIDE as guide } from "../../guides";

export const metadata = guideMetadata(guide);

export default function Layout({ children }: { children: ReactNode }) {
  return <GuideLayout guide={guide}>{children}</GuideLayout>;
}
