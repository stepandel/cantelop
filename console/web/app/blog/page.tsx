import type { Metadata } from "next";
import Link from "next/link";

import { markdownSources } from "@/components/markdown/source";
import { POST_ART } from "./post-art";
import { POSTS } from "./posts";
import styles from "./blog.module.css";

export const metadata: Metadata = {
  title: "Blog",
  alternates: { canonical: "https://cantelop.com/blog" },
  description: "Notes on the design of Cantelop: how the platform runs cloud agents, and why it is built the way it is.",
};

/* Reading time from the post's own markdown, at an unhurried pace. */
function minutesToRead(markdown: string | undefined) {
  const words = markdown?.split(/\s+/).length ?? 0;
  return Math.max(1, Math.round(words / 200));
}

export default function BlogIndex() {
  const sources = markdownSources("web/app/blog/(posts)", "/blog");

  return (
    <div className={styles.index}>
      <h1 className={styles.indexTitle}>Blog</h1>

      <div className={styles.cards}>
        {POSTS.map((post) => {
          const Art = POST_ART[post.slug];
          return (
            <article key={post.slug} className={styles.card}>
              <Link className={styles.cardLink} href={`/blog/${post.slug}`}>
                <div className={styles.art} aria-hidden="true">
                  {Art ? <div className={styles.artInner}><Art /></div> : null}
                </div>
                <div className={styles.cardBody}>
                  <time className={styles.cardDate} dateTime={post.date}>{post.date}</time>
                  <h2 className={styles.cardTitle}>{post.title}</h2>
                </div>
              </Link>
              <div className={styles.cardFoot}>
                <a
                  className={styles.author}
                  href={post.author.url}
                  target="_blank"
                  rel="noopener noreferrer"
                >
                  <img className={styles.avatar} src={post.author.avatar} alt="" width={20} height={20} />
                  {post.author.name}
                </a>
                <span className={styles.metaDot} aria-hidden="true">•</span>
                <span>{minutesToRead(sources[`/blog/${post.slug}`])} min read</span>
              </div>
            </article>
          );
        })}
      </div>
    </div>
  );
}
