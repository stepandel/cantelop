import type { MetadataRoute } from "next";

export default function robots(): MetadataRoute.Robots {
  return {
    rules: {
      userAgent: "*",
      allow: "/",
      disallow: ["/admin/", "/api/", "/apps/", "/dashboard/", "/invitations/", "/preview/", "/profile/", "/v1/"],
    },
    sitemap: "https://cantelop.com/sitemap.xml",
  };
}
