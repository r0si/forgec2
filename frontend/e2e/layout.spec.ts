import { expect, test, type Page, type Route } from "@playwright/test";

// Top-10 operator routes: the seven original surfaces plus the three
// data-dense pages (loot, credentials, payload-generator) that are the most
// likely to overflow narrow viewports.
const ROUTES = ["dashboard", "beacons", "listeners", "events", "loot", "credentials", "payload-generator", "settings", "ai", "chat"] as const;
const VISUAL_ROUTES = ["login", "dashboard", "beacons", "listeners", "events", "settings", "ai"] as const;

const envelope = {
  status: "ok",
  success: true,
  version: "test",
  data: {
    username: "admin",
    role: "admin",
    permissions: ["agents.read", "listeners.read", "settings.read", "ai.configure"],
    profiles: [],
    agents: [],
  },
  agents: [],
  tasks: [],
  listeners: [],
  notifications: [],
  sessions: [],
  channels: [],
  total: 0,
  CurrentUsername: "admin",
  CurrentUserRole: "admin",
};

async function prepare(page: Page, theme: "light" | "dark" = "light") {
  await page.addInitScript((selectedTheme) => {
    localStorage.setItem("forgec2_theme", selectedTheme);
    localStorage.setItem("forgec2_lang", "en");
  }, theme);
  await page.context().addCookies([
    { name: "forgec2_session", value: "layout-e2e", domain: "127.0.0.1", path: "/" },
  ]);
  await page.route("**/*", async (route: Route) => {
    const type = route.request().resourceType();
    if (type === "websocket") return route.abort();
    if (type === "xhr" || type === "fetch") {
      const pathname = new URL(route.request().url()).pathname;
      if ([
        "/ai/sessions",
        "/api/ai/profiles",
        "/api/ai/runs",
        "/api/ai/intents",
        "/api/ai/knowledge/collections",
      ].includes(pathname) || /^\/api\/ai\/sessions\/[^/]+\/attachments$/.test(pathname)) {
        return route.fulfill({ status: 200, contentType: "application/json", body: "[]" });
      }
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(envelope) });
    }
    return route.continue();
  });
}

async function expectNoPageOverflow(page: Page) {
  const overflow = await page.evaluate(() => ({
    viewport: document.documentElement.clientWidth,
    document: document.documentElement.scrollWidth,
    body: document.body.scrollWidth,
  }));
  expect(overflow.document, JSON.stringify(overflow)).toBeLessThanOrEqual(overflow.viewport + 1);
  expect(overflow.body, JSON.stringify(overflow)).toBeLessThanOrEqual(overflow.viewport + 1);
}

/** Routes the SPA rewrites client-side; the requested file is not the surface. */
const REDIRECT_ROUTES: Partial<Record<(typeof ROUTES)[number], string>> = {
  chat: "/ai",
};

async function waitForPageSurface(page: Page, route: typeof ROUTES[number] | "login") {
  if (route === "login") {
    await expect(page.getByRole("form")).toBeVisible();
    return;
  }
  // Waiting for <main> only proves that the app shell mounted. Several pages
  // resolve permissions and initial data one tick later, which previously
  // produced misleading blank visual baselines on mobile.
  const redirect = REDIRECT_ROUTES[route];
  if (redirect) {
    await page.waitForURL((url) => url.pathname.replace(/\.html$/, "") === redirect || `${url.pathname}.html`.replace(/\.html\.html$/, ".html") === redirect, {
      timeout: 15_000,
    });
    await page.waitForTimeout(300);
  }
  await expect(page.locator("main h1").first()).toBeAttached({ timeout: 15_000 });
  // Lazy chunks (charts, AI panels) resolve after the shell paints. Under the
  // parallel CI load they can land between the height-settle and the capture,
  // which shows up as a few thousand stray pixels. Wait for the network to go
  // quiet first, then settle the height.
  await page.waitForLoadState("networkidle");
  // Settle the document height across two frames so a stitching fullPage
  // screenshot doesn't capture two offset copies during a layout shift.
  await page.evaluate(
    () =>
      new Promise<void>((resolve) => {
        let last = -1;
        let stable = 0;
        const tick = () => {
          const h = document.documentElement.scrollHeight;
          stable = h === last ? stable + 1 : 0;
          last = h;
          if (stable >= 2) resolve();
          else requestAnimationFrame(tick);
        };
        requestAnimationFrame(tick);
      }),
  );
}

test.describe("responsive shell", () => {
  test("login remains centered and does not overflow", async ({ page }) => {
    await prepare(page);
    await page.goto("/login.html");
    await expect(page.getByRole("form")).toBeVisible();
    await expectNoPageOverflow(page);
  });

  for (const route of ROUTES) {
    test(`${route} uses a bounded page shell`, async ({ page }) => {
      await prepare(page);
      await page.goto(`/${route}.html`);
      await waitForPageSurface(page, route);
      await expectNoPageOverflow(page);
    });
  }

  test("AI configuration opens without replacing the conversation", async ({ page }) => {
    await prepare(page);
    await page.goto("/ai.html");
    await waitForPageSurface(page, "ai");
    await page.getByRole("button", { name: /AI Configuration|AI 配置/i }).click();
    await expect(page.getByRole("heading", { name: /AI Configuration|AI 配置/i })).toBeVisible();
    await expect(page.locator("main h1")).toBeAttached();
  });

  test("connection status stays readable as text at every width", async ({ page }) => {
    // R2: the TopBar used to hide the live/offline/reconnecting label below lg,
    // leaving a bare color dot (WCAG 1.4.1) on phones and tablets.
    await prepare(page);
    await page.goto("/dashboard.html");
    const status = page.locator("header [role='status']");
    await expect(status).toBeVisible();
    await expect(status).toContainText(/Live|Offline|Reconnecting/);
  });
});

test.describe("route transitions", () => {
  // M1: the router wraps every client-side navigation in
  // document.startViewTransition (globals.css owns the animation). Count the
  // invocations without changing the transition itself.
  async function installTransitionCounter(page: Page) {
    await page.addInitScript(() => {
      const state = window as unknown as { __vtCalls?: number };
      state.__vtCalls = 0;
      if (typeof document.startViewTransition !== "function") return;
      const native = document.startViewTransition.bind(document);
      document.startViewTransition = (callbackOrOptions) => {
        state.__vtCalls = (state.__vtCalls ?? 0) + 1;
        return native(callbackOrOptions);
      };
    });
  }

  async function loadChatRedirect(page: Page) {
    // /chat is rewritten client-side to /ai by the route table (see
    // REDIRECT_ROUTES above): a deterministic router navigation that needs no
    // operator interaction, so the assertion cannot drift with page data or
    // viewport-specific chrome.
    await page.goto("/chat.html");
    await page.waitForURL(/\/ai/);
  }

  async function transitionCalls(page: Page) {
    return page.evaluate(() => (window as unknown as { __vtCalls?: number }).__vtCalls);
  }

  test("client navigation opts into the View Transitions API", async ({ page }) => {
    await prepare(page);
    await installTransitionCounter(page);
    await loadChatRedirect(page);
    // Poll: the counter is installed by an init script but the router may call
    // it a tick after waitForURL resolves, which flaked under parallel load.
    await expect.poll(() => transitionCalls(page)).toBeGreaterThan(0);
  });

  test("reduced motion keeps the instant swap", async ({ page }) => {
    await prepare(page);
    await page.emulateMedia({ reducedMotion: "reduce" });
    await installTransitionCounter(page);
    await loadChatRedirect(page);
    // Give the client navigation a moment to finish before asserting the
    // counter stayed at zero.
    await page.waitForTimeout(300);
    expect(await transitionCalls(page)).toBe(0);
  });
});

test.describe("light and dark visual baselines", () => {
  for (const route of VISUAL_ROUTES) {
    for (const theme of ["light", "dark"] as const) {
      test(`${route} ${theme}`, async ({ page }, testInfo) => {
        test.skip(!["mobile", "laptop"].includes(testInfo.project.name), "Visual baselines use representative mobile and laptop widths");
        // Baselines are per-platform because the font stack changes the
        // rendering: *-win32.png is rendered by Playwright on Windows and
        // *-linux.png on ubuntu-latest (the same image the frontend job uses).
        // Both sets are committed, so the visual contract is enforced in CI
        // and locally; other platforms skip instead of failing on a missing
        // snapshot. Regenerate linux via .github/workflows/visual-baselines.yml
        // and win32 with `npx playwright test e2e/layout.spec.ts
        // --update-snapshots=all` on Windows (plain --update-snapshots only
        // rewrites snapshots whose diff exceeds maxDiffPixelRatio).
        test.skip(
          !["win32", "linux"].includes(process.platform),
          "Visual baselines exist for win32 and linux only",
        );
        await prepare(page, theme);
        await page.goto(`/${route}.html`);
        await waitForPageSurface(page, route);
        await expect(page).toHaveScreenshot(`${route}-${theme}.png`, {
          animations: "disabled",
          fullPage: true,
          maxDiffPixelRatio: 0.015,
        });
      });
    }
  }
});
