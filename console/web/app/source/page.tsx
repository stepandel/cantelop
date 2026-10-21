import type { Metadata } from "next";

import { SiteFooter } from "@/components/site-footer";
import { SiteTopbar } from "@/components/site-topbar";
import { sourceInfo } from "@/lib/source-info";
import styles from "../privacy/privacy.module.css";

export const metadata: Metadata = { title: "Source code" };
export const dynamic = "force-dynamic";

// A misconfigured source offer should fall back to asking the operator, not fail the page.
function deploymentSource() {
  try {
    return sourceInfo();
  } catch (error) {
    console.error("source offer is misconfigured", error);
    return null;
  }
}

export default function SourcePage() {
  const source = deploymentSource();
  return <main className="site">
    <SiteTopbar />
    <article className={styles.article}>
      <h1>Source code</h1>
      <p>Cantelop platform code is licensed under the GNU Affero General Public License version 3, with the exceptions described in its license map. The separate Cantelop SDK and supplied application starters are MIT licensed.</p>
      {source ? <>
        <p><a href={source.sourceURL}>Browse the source for this console deployment</a>.</p>
        <p>Revision: <code>{source.commit}</code></p>
        <p><a href={`${source.repository}/blob/${source.commit}/LICENSE`}>Platform license</a> · <a href={`${source.repository}/blob/${source.commit}/docs/licensing.md`}>License map</a> · <a href={`${source.repository}/blob/${source.commit}/THIRD_PARTY_NOTICES.md`}>Third-party notices</a></p>
      </> : <p>The operator has not configured a valid source revision for this deployment. Ask the operator for the corresponding source.</p>}
      <p>Source for other Cantelop components is identified by the <code>SOURCE.json</code> files shipped with their release archives. Operators of modified deployments must provide source matching the components they run.</p>
    </article>
    <SiteFooter />
  </main>;
}
