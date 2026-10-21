"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import { POSTS } from "./posts";
import styles from "./blog.module.css";

const DATE_FORMAT = new Intl.DateTimeFormat("en-US", {
  month: "long",
  day: "numeric",
  year: "numeric",
  timeZone: "UTC",
});

/* A post's complete masthead. Keeping it in the posts layout gives every
   article the same hierarchy without repeating header markup in MDX. */
export function PostHeader() {
  const pathname = usePathname();
  const post = POSTS.find(({ slug }) => pathname === `/blog/${slug}`);
  if (!post) {
    return null;
  }

  return (
    <header className={styles.postHeader}>
      <div className={styles.postContext}>
        <Link className={styles.backLink} href="/blog">
          <span aria-hidden="true">←</span> Blog
        </Link>
        <time dateTime={post.date}>
          {DATE_FORMAT.format(new Date(`${post.date}T00:00:00Z`))}
        </time>
      </div>
      <h1 className={styles.postTitle}>{post.title}</h1>
      <a
        className={styles.author}
        href={post.author.url}
        target="_blank"
        rel="noopener noreferrer"
      >
        <img className={styles.avatar} src={post.author.avatar} alt="" width={20} height={20} />
        {post.author.name}
      </a>
    </header>
  );
}
