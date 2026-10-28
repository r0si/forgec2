import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { ROUTE_PAGE_IMPORTS } from "./route-imports";
import { NAV_SECTIONS } from "./navigation";

// A nav entry whose path has no page is a dead link that only shows up when an operator clicks it.
// The router builds every page path from ROUTE_PAGE_IMPORTS (`/<id>`) plus a few explicit redirects,
// so the two lists are the whole story and can be cross-checked against the sidebar.

const routerSource = readFileSync(join(process.cwd(), "src", "router.tsx"), "utf8");

/** Every explicit path the router declares outside the page map (redirects and legacy aliases). */
function declaredPaths(): Set<string> {
  const paths = new Set<string>();
  for (const m of routerSource.matchAll(/path:\s*"(\/[^"]*)"/g)) paths.add(m[1]);
  return paths;
}

const pagePaths = new Set(Object.keys(ROUTE_PAGE_IMPORTS).map((id) => `/${id}`));

describe("ROUTE_PAGE_IMPORTS", () => {
  it("uses route ids that are usable as URL segments", () => {
    for (const id of Object.keys(ROUTE_PAGE_IMPORTS)) {
      // The router turns each key into `/<id>` verbatim.
      expect(id).toMatch(/^[a-z0-9]+(-[a-z0-9]+)*$/);
    }
  });

  // 30s: this imports EVERY page module, and under the full parallel suite the dynamic imports pull
  // the large vendor chunks too, which blows the 5s default. It passed in isolation (1-2s) and timed
  // out in `npm run test` - a flaky guard is worse than none.
  it("maps every id to a lazy loader that returns a promise", async () => {
    for (const [id, load] of Object.entries(ROUTE_PAGE_IMPORTS)) {
      expect(typeof load, id).toBe("function");
      const mod = await load();
      expect(mod, id).toBeTruthy();
      expect("default" in mod, `${id} has no default export`).toBe(true);
    }
  }, 30_000);
});

describe("sidebar navigation", () => {
  it("only links to paths the router can serve", () => {
    const declared = declaredPaths();
    const dead: string[] = [];
    for (const section of NAV_SECTIONS) {
      for (const item of section.items) {
        const path = item.href.split(/[?#]/)[0];
        const firstSegment = `/${path.split("/").filter(Boolean)[0] ?? ""}`;
        if (!pagePaths.has(firstSegment) && !declared.has(firstSegment) && !declared.has(path)) {
          dead.push(`${item.href} (${item.labelKey})`);
        }
      }
    }
    expect(dead, `nav entries with no route: ${dead.join(", ")}`).toEqual([]);
  });

  it("has no duplicate hrefs across sections", () => {
    const seen = new Map<string, string>();
    const duplicates: string[] = [];
    for (const section of NAV_SECTIONS) {
      for (const item of section.items) {
        const previous = seen.get(item.href);
        if (previous) duplicates.push(`${item.href} in ${previous} and ${section.titleKey}`);
        seen.set(item.href, section.titleKey);
      }
    }
    expect(duplicates, duplicates.join("; ")).toEqual([]);
  });
});
