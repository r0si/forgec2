import { fileURLToPath, URL } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";
import { staticExportPlugin } from "./vite-plugins/staticExport.js";

// Inject <link rel="preload" as="font"> for the hashed woff2 assets emitted
// during the build. The hashed filename is only known after bundling, so the
// tags are appended in transformIndexHtml (build mode) from ctx.bundle.
function fontPreloadPlugin(): Plugin {
  return {
    name: "forgec2:font-preload",
    apply: "build",
    transformIndexHtml: {
      order: "post",
      handler(_html, ctx) {
        const bundle = ctx.bundle;
        if (!bundle) return [];
        return Object.keys(bundle)
          // Inter is used by the application shell. JetBrains Mono is only
          // needed by terminal/code surfaces, so let the browser fetch it
          // when those surfaces are actually rendered.
          .filter((f) => f.endsWith(".woff2") && f.includes("Inter-Variable"))
          .map((f) => ({
            tag: "link",
            attrs: {
              rel: "preload",
              as: "font",
              type: "font/woff2",
              crossorigin: "",
              href: "/" + f,
            },
          }));
      },
    },
  };
}

// base-ui subpaths that no entry-reachable module imports. Measured with a
// per-subpath build (every subpath in its own chunk) and the entry's static
// import closure over the emitted files; the list is the set of chunks that
// closure never reached. See the manualChunks comment below.
//
// The 1.9.0 cycle that forced the ~1.6.0 pin ("Cannot access 'we' before
// initialization" via vendor-baseui <-> vendor-baseui-forms) is gone under
// Vite 8/Rolldown: the two chunks no longer import each other (verified 0/0)
// and the 1.9.0 e2e run passes. The set still keeps page-only subpaths out of
// first paint; re-measure it if a future base-ui reshuffles subpaths.
const BASEUI_PAGE_ONLY_KEYS = new Set([
  "accordion",
  "checkbox",
  "checkbox-group",
  "direction-provider",
  "error.mjs",
  "fieldset",
  "getDefaultFormSubmitter.mjs",
  "popover",
  "radio",
  "radio-group",
  "switch",
  "tabs",
  "useForcedRerendering.mjs",
  "warn.mjs",
]);

// Icons no first-paint screen renders. Measured 2026-11-09 by building with one
// chunk per icon module and walking the entry's static closure: 56 icons
// (10 kB) are entry-reachable, the 170 below are not, and the single bucket
// they used to share charged all 67 kB to first paint. Icons not listed here
// stay in vendor-icons (the entry bucket): when a new one is added it costs
// ~200 B, and if it is page-only, add it here to move it out.
const ICON_PAGE_ONLY = new Set([
  "anchor", "app-window", "apple", "arrow-down", "arrow-left", "arrow-right",
  "arrow-up", "arrow-up-down", "award", "badge-info", "ban", "binary",
  "book-open", "bookmark", "bookmark-plus", "box", "brain", "calendar",
  "calendar-check", "calendar-clock", "camera", "chart-column", "chart-no-axes-column", "chart-pie",
  "check-check", "chevron-left", "chevron-right", "chevrons-up-down", "circle", "circle-alert",
  "circle-check", "circle-check-big", "circle-dot", "circle-plus", "circle-question-mark", "circle-x",
  "clipboard", "clipboard-paste", "clock-3", "cloud-upload", "code-xml", "cog",
  "columns-2", "compass", "copy", "cpu", "credit-card", "crown",
  "database", "disc", "download", "earth", "eighth-notes", "ellipsis",
  "eraser", "expand", "external-link", "eye", "eye-off", "file",
  "file-braces", "file-braces-corner", "file-code-corner", "file-down", "file-exclamation-point", "file-spreadsheet",
  "file-text", "file-up", "flame", "flask-conical", "folder", "folder-open",
  "folder-plus", "folder-tree", "folder-up", "funnel", "gauge", "git-compare",
  "globe", "grid-3x3", "grip", "hammer", "hand", "hard-drive",
  "heart-pulse", "house", "image", "images", "import", "inbox",
  "info", "key-round", "languages", "laptop", "lightbulb", "link-2",
  "list", "list-checks", "list-filter", "list-todo", "list-x", "loader-circle",
  "lock", "lock-open", "mail", "map-pin", "megaphone", "message-circle",
  "message-square-text", "mic", "mouse", "mouse-pointer-click", "package", "package-open",
  "palette", "paperclip", "pause", "paw-print", "pen-line", "pencil",
  "picture-in-picture-2", "pin", "pin-off", "play", "plus", "power",
  "printer", "radar", "refresh-cw", "rocket", "rotate-ccw", "rotate-ccw-clock",
  "rows-2", "rows-3", "save", "scan-search", "search-x", "send",
  "share-2", "shield-check", "shield-off", "shield-user", "shield-x", "skull",
  "sliders-horizontal", "snowflake", "sparkles", "square", "square-terminal", "star",
  "sticky-note", "syringe", "table-2", "tag", "target", "ticket",
  "timer", "tower-control", "trash", "trending-up", "upload", "usb",
  "user", "user-check", "user-cog", "video", "wallet", "waypoints",
  "wifi", "workflow",
]);

export default defineConfig(({ command }) => {
  // One-release compat: the old NEXT_PUBLIC_* names are ignored; warn loudly
  // instead of failing silently with an empty API base.
  for (const legacy of ["NEXT_PUBLIC_API_BASE", "NEXT_PUBLIC_WS_URL", "NEXT_PUBLIC_GO_BACKEND_PORT"]) {
    if (process.env[legacy]) {
      console.warn(
        `[forgec2] ${legacy} is no longer read; use VITE_FORGEC2_${legacy.slice("NEXT_PUBLIC_".length)} instead.`,
      );
    }
  }
  return {
  plugins: [
    react(),
    fontPreloadPlugin(),
    ...(command === "build" ? [staticExportPlugin()] : []),
  ],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
      // The English dictionary is ~239 kB — the largest chunk on the
      // first-paint path. A production build resolves this alias to an empty
      // module so `./en` is only reachable through the provider's dynamic
      // import (its own chunk); dev and test keep the synchronous dictionary
      // so tests can assert translated text immediately.
      "@i18n-sync": fileURLToPath(
        new URL(
          command === "build" ? "./src/lib/i18n/en.sync.prod.ts" : "./src/lib/i18n/en.sync.ts",
          import.meta.url,
        ),
      ),
    },
  },
  build: {
    outDir: "out",
    emptyOutDir: true,
    assetsDir: "assets",
    target: "es2020",
    chunkSizeWarningLimit: 500,
    // Safari 16 (2022) is the CSS floor. It matters for reproducibility,
    // not for support: lightningcss downgrades the oklch() palette to lab()
    // for browsers that only speak lab, and prints the converted numbers with
    // a platform-dependent precision (36.4219 on Windows, 36.422 on Linux).
    // One byte changes the CSS file's hash, which changes the name of every
    // chunk importing it and of every HTML page referencing it - the
    // committed bundle could then never match the CI's Linux build (424
    // diffs). With oklch supported natively there is no downgrade to print,
    // so the CSS is byte-identical on every platform.
    cssTarget: "safari16",
    rolldownOptions: {
      output: {
        manualChunks(id) {
          // NOTE: no page-directory buckets on purpose.
          //
          // Bucketing whole directories (id.includes("src/pages/agents"))
          // ties a chunk's fate to the one entry-reachable module that happens
          // to live under that path: a single such module makes the whole
          // bucket a STATIC dependency of the entry, so the browser fetches it
          // before first paint. Measured here, the entry pulled
          // route-agents-list + route-agents-detail + route-ai + route-generate
          // + route-credentials = 928 kB of route JS that no first-paint
          // screen uses.
          //
          // Every page is already reached through lazy(() => import(...)),
          // and rollup splits at those boundaries on its own - finer-grained
          // than the directory buckets were (one chunk per page component).
          // Measured before/after with identical method: the first-paint
          // critical path fell from 1682 kB to 1076 kB, with route-* chunks in
          // it going from 906 kB to zero. Total JS+CSS rose 30 kB (3.92 ->
          // 3.95 MB) and the file count went 107 -> 183: a little more code is
          // now paid per route visit instead of by every operator on every
          // first paint. The vendor buckets below stay - they are what stops
          // react being duplicated across chunks.
          if (!id.includes("node_modules")) {
            // Page-only English dictionaries (measured 2026-11: agents 53 kB,
            // settings 26 kB). They are plain data modules registered by the
            // route entry that renders them; keeping each in its own chunk is
            // what stops `en-*.js` (165 kB) from carrying both. Do NOT let the
            // fragment modules import the provider/registry: Rolldown then
            // merges i18n/index.tsx into these chunks (72 kB) and preloads the
            // whole thing again, which is exactly what this split removes.
            if (id.includes("lib/i18n/fragments/")) {
              if (id.includes("en.agents")) return "i18n-agents";
              if (id.includes("en.settings")) return "i18n-settings";
              if (id.includes("en.generate")) return "i18n-generate";
            }
            return;
          }
          // Order matters: react-router is a react lib too.
          if (id.includes("react-router")) return "vendor-router";
          if (id.includes("@xterm")) return "vendor-xterm";
          // CodeMirror is reached only from the lazily-loaded profile editor.
          // Without a dedicated bucket its transitive deps fall into
          // vendor-misc, which the entry imports for other packages, so the
          // whole editor would be fetched before first paint.
          if (
            id.includes("@codemirror") ||
            id.includes("@lezer") ||
            id.includes("@marijn") ||
            id.includes("style-mod") ||
            id.includes("w3c-keyname") ||
            id.includes("/crelt/")
          ) {
            return "vendor-codemirror";
          }
          if (id.includes("@base-ui")) {
            // base-ui ships one subpath per component. The shell only needs the
            // interaction primitives (tooltip/dialog/menu/select/input/button/
            // progress/collapsible + their shared internals); the form controls,
            // tabs, popover and accordion are reached exclusively from
            // lazily-loaded pages. Bucketing the whole package into one
            // vendor chunk put all of it into first paint, so the page-only
            // subpaths get their own bucket that only lazy page chunks import.
            // Membership is empirical: the key set below is exactly the set of
            // chunks unreachable from the entry (measured with a per-subpath
            // build via the same key extraction; check:bundle fails if a
            // "forms" subpath ever becomes entry-reachable again).
            const key = id.match(/@base-ui\/(?:react|utils)\/([^/]+)/)?.[1] ?? "";
            return BASEUI_PAGE_ONLY_KEYS.has(key) ? "vendor-baseui-forms" : "vendor-baseui";
          }
          if (id.includes("lucide-react")) {
            // Icons are one module each. A single bucket charged the entry for
            // every icon any page imports (67 kB) because the shell's icons
            // shared it. The page-only names below are measured: build with one
            // chunk per icon, walk the entry's static closure, and the pages'
            // icons are the complement of the 56 entry icons (10 kB). An icon
            // that is not listed here stays in vendor-icons — a new shell icon
            // costs ~200 B instead of silently pulling the whole page-only set
            // into first paint; listing a new page-only icon reclaims it.
            const icon = id.match(/lucide-react\/dist\/esm\/icons\/([^/]+)\.mjs$/)?.[1];
            if (icon && ICON_PAGE_ONLY.has(icon)) return "vendor-icons-pages";
            return "vendor-icons";
          }
          if (id.includes("dompurify")) return "vendor-dompurify";
          if (id.includes("html-to-image")) return "vendor-html2image";
          if (id.includes("zod")) return "vendor-zod";
          if (id.includes("sonner")) return "vendor-sonner";
          // react-interop CJS packages (use-sync-external-store etc.) do a
          // top-level require("react"); they MUST live in the same chunk as
          // react itself or the circular chunk import crashes at boot
          // ("Cannot set properties of undefined (setting 'Activity')").
          if (id.includes("use-sync-external-store") || id.includes("react-is")) return "vendor-react";
          if (id.includes("react") || id.includes("scheduler")) return "vendor-react";
          // web-vitals is reached only from the performance page. Without its
          // own bucket it lands in vendor-misc, which the entry imports, so
          // every visitor downloaded it before first paint (measured 9 kB).
          if (id.includes("web-vitals")) return "vendor-misc-pages";
          return "vendor-misc";
        },
      },
    },
  },
  server: {
    proxy: {
      "/api": { target: "http://127.0.0.1:8000", changeOrigin: true },
      "/login": { target: "http://127.0.0.1:8000", changeOrigin: true },
      "/extc2": { target: "http://127.0.0.1:8000", changeOrigin: true },
      "/ws": { target: "ws://127.0.0.1:8000", ws: true },
    },
  },
  };
});
