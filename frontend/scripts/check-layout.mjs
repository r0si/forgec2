#!/usr/bin/env node
// Layout gate (U9.7/U9.5): the layout contract in docs/frontend-style-conventions.md
// is only worth writing down if it is enforced. These rules catch the
// ways the contract was broken in the first place - each one reachable with
// a single class name.
//
//   node scripts/check-layout.mjs            # report + exit 1 on findings
//   node scripts/check-layout.mjs --list     # list the findings only
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";

const ROOT = join(process.cwd(), "src");
const listOnly = process.argv.includes("--list");

/** Agent workspaces share one chrome contract: workspace container + toolbar. */
const WORKSPACE_PAGES = new Map([
  ["pages/beacons/detail/files/FilesPage.tsx", "<FilesToolbar"],
  ["pages/beacons/detail/shell/ShellPage.tsx", "<WorkspaceToolbar"],
  ["pages/beacons/detail/screen/ScreenPage.tsx", "<ScreenToolbar"],
  ["pages/beacons/detail/remote-desktop/RemoteDesktopPage.tsx", "<RdpToolbar"],
]);

/** The page-specific toolbar wrappers must delegate to the shared primitive. */
const WORKSPACE_TOOLBARS = new Set([
  "pages/beacons/detail/files/components/FilesToolbar.tsx",
  "pages/beacons/detail/screen/components/ScreenToolbar.tsx",
  "pages/beacons/detail/remote-desktop/components/RdpToolbar.tsx",
]);

/** @type {{file: string, line: number, rule: string, text: string, fix: string}[]} */
const findings = [];

function walk(dir) {
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    const st = statSync(full);
    if (st.isDirectory()) {
      if (entry === "node_modules" || entry === "dist" || entry === "out") continue;
      walk(full);
      continue;
    }
    if (!/\.tsx?$/.test(entry)) continue;
    if (/\.(test|spec)\.tsx?$/.test(entry)) continue;
    check(full);
  }
}

function report(file, line, rule, text, fix) {
  findings.push({ file: relative(ROOT, file).replace(/\\/g, "/"), line, rule, text, fix });
}

function check(file) {
  const src = readFileSync(file, "utf8");
  const lines = src.split("\n");
  const rel = relative(ROOT, file).replace(/\\/g, "/");
  lines.forEach((line, i) => {
    // Rule 1: a dialog's width belongs to its `size` prop. Only the
    // element's own tag is inspected - a width class on a child of the
    // dialog (an image stage, say) is none of the gate's business.
    if (line.includes("<DialogContent")) {
      let tag = line;
      for (let k = i; k < lines.length && !tag.includes(">"); k++) tag += " " + lines[k];
      const m = tag.match(/className="([^"]*)"/);
      if (m) {
        const bad = m[1].split(/\s+/).filter((t) => /^(?:sm:|md:|lg:|xl:)?max-w-/.test(t) && t !== "max-w-[calc(100%-2rem)]");
        if (bad.length) {
          report(file, i + 1, "dialog.width", bad.join(" "), "use <DialogContent size=\"lg\"> instead of a width class");
        }
      }
    }
    // Rule 2: a card's interior padding belongs to --card-spacing.
    if (line.includes("<Card") && !line.includes("<CardHeader") && !line.includes("<CardContent") && !line.includes("<CardFooter")) {
      const m = line.match(/className="([^"]*)"/);
      if (m) {
        const bad = m[1].split(/\s+/).filter((t) => /^(?:sm:|md:|lg:|xl:)?p-(?!0$)\d+(?:\.\d+)?$/.test(t));
        if (bad.length) {
          report(file, i + 1, "card.padding", bad.join(" "), "use p-(--card-spacing) (p-0 stays for flush tables)");
        }
      }
    }
    // Rule 3: Tailwind v4 does not compile space-y with a custom property.
    if (/space-y-\(--/.test(line)) {
      report(file, i + 1, "space-y.custom-property", "space-y-(--…)", "Tailwind v4 emits no rule for space-y with a custom property - use gap-(--page-gap) on a flex column");
    }
  });

  // Rule 5: the four agent workspaces use the shared container + toolbar.
  // Padding belongs to the content region so the toolbar can stay edge-to-edge.
  if (WORKSPACE_PAGES.has(rel)) {
    if (!src.includes('variant="workspace"')) {
      report(file, 1, "workspace.variant", rel, 'workspace pages must render <PageContainer variant="workspace">');
    }
    const toolbarTag = WORKSPACE_PAGES.get(rel);
    if (!src.includes(toolbarTag)) {
      report(file, 1, "workspace.toolbar", rel, `workspace pages must render ${toolbarTag} instead of a hand-rolled header`);
    }
    const pc = src.match(/<PageContainer[\s\S]*?>/);
    if (pc && /(?:px-\(--page-pad-x\)|py-\(--page-pad-y\))/.test(pc[0])) {
      const line = src.slice(0, pc.index).split("\n").length;
      report(file, line, "workspace.padding", "PageContainer padding", "move --page-pad-* to the scrolling content region; the toolbar owns the band");
    }
  }

  if (WORKSPACE_TOOLBARS.has(rel) && !src.includes("<WorkspaceToolbar")) {
    report(file, 1, "workspace.toolbar_primitive", rel, "workspace toolbars must render <WorkspaceToolbar> instead of a hand-rolled header");
  }

  // Rule 6: detail sections use the SectionCard/CollectCard shell. A direct
  // Card + CardHeader import is the exact drift this rule exists to stop.
  if (rel.startsWith("pages/beacons/detail/") && src.includes('"@/components/ui/card-header"')) {
    const line = src.slice(0, src.indexOf('"@/components/ui/card-header"')).split("\n").length;
    report(file, line, "detail.card_header", "CardHeader", "use <SectionCard> (or <CollectCard> for collect sections) instead of a hand-rolled Card + CardHeader");
  }

  // Rule 7: the label's typography belongs to the Label primitive. The tag can
  // span lines, so this pass reads the whole file.
  const labelRe = /<Label(\s[^>]*?)className="([^"]*)"/g;
  let m;
  while ((m = labelRe.exec(src)) !== null) {
    const owned = m[2].split(/\s+/).filter((t) => ["text-xs", "font-medium", "text-muted-foreground"].includes(t));
    if (owned.length) {
      const line = src.slice(0, m.index).split("\n").length;
      report(file, line, "label.typography", owned.join(" "), "the Label primitive owns the label typography - keep only the layout classes");
    }
  }
}

walk(ROOT);

if (findings.length === 0) {
  console.log("[layout] layout contract OK - dialogs declare a size, cards use --card-spacing, workspaces share one toolbar, page rhythm uses the token.");
  process.exit(0);
}

for (const f of findings) {
  console.error(`[layout] ${f.file}:${f.line}  ${f.rule}  ${f.text}\n         -> ${f.fix}`);
}
console.error(`[layout] ${findings.length} layout finding(s).`);
if (!listOnly) process.exit(1);
process.exit(0);
