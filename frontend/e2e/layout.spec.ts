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

async function prepare(
  page: Page,
  theme: "light" | "dark" = "light",
  extraPermissions: string[] = [],
  lang: "en" | "zh" = "en",
) {
  const permissions = [...envelope.data.permissions, ...extraPermissions];
  const body = JSON.stringify({ ...envelope, data: { ...envelope.data, permissions } });
  await page.addInitScript(
    ({ selectedTheme, selectedLang }) => {
      localStorage.setItem("forgec2_theme", selectedTheme);
      localStorage.setItem("forgec2_lang", selectedLang);
    },
    { selectedTheme: theme, selectedLang: lang },
  );
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
      return route.fulfill({ status: 200, contentType: "application/json", body });
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

  test("the login page never downloads the full English dictionary", async ({ page }) => {
    await prepare(page);
    const enChunks: string[] = [];
    const zodChunks: string[] = [];
    page.on("request", (request) => {
      const pathname = new URL(request.url()).pathname;
      if (/\/assets\/en-[\w.-]+\.js$/.test(pathname)) enChunks.push(request.url());
      // The login form's two `required` checks must not pull zod's 84 kB.
      if (/\/assets\/vendor-zod-[\w.-]+\.js$/.test(pathname)) zodChunks.push(request.url());
    });
    await page.goto("/login.html");
    await expect(page.getByRole("form")).toBeVisible();
    // The provider fetches on mount; give that effect time to fire before
    // asserting the absence of the request.
    await page.waitForTimeout(600);
    expect(enChunks, "login page fetched the full en dictionary").toHaveLength(0);
    expect(zodChunks, "login page fetched vendor-zod").toHaveLength(0);

    // Authenticated routes still load it: the dictionary is deferred until
    // after sign-in, not dropped.
    await page.goto("/dashboard.html");
    await expect(page.locator("main h1").first()).toBeAttached({ timeout: 15_000 });
    await expect.poll(() => enChunks.length, { timeout: 10_000 }).toBeGreaterThan(0);
  });

  test("page-only dictionary fragments load with their own pages", async ({ page }) => {
    // credentials.read is only needed for the /credentials leg below; the
    // visual-baseline tests keep the smaller permission set so their sidebar
    // matches the committed screenshots.
    await prepare(page, "light", [
      "credentials.read",
      "plugins.read",
      "campaigns.read",
      "opsec.read",
      "automation.read",
      "users.read",
    ]);
    const dictChunks: string[] = [];
    page.on("request", (request) => {
      const pathname = new URL(request.url()).pathname;
      if (/\/assets\/i18n-[\w.-]+\.js$/.test(pathname)) dictChunks.push(pathname);
    });

    // Beacon pages register the agents.fragment; the settings page registers
    // settings. Neither is in the startup chunk (en-*.js is).
    await page.goto("/beacons.html");
    await expect(page.locator("main").first()).toBeAttached({ timeout: 15_000 });
    await expect.poll(() => dictChunks.some((c) => c.includes("i18n-agents"))).toBe(true);
    await expect(page.locator("main")).not.toContainText(/\bagents\.[a-z_]/);

    dictChunks.length = 0;
    await page.goto("/settings.html");
    await expect(page.locator("main").first()).toBeAttached({ timeout: 15_000 });
    await expect.poll(() => dictChunks.some((c) => c.includes("i18n-settings"))).toBe(true);
    await expect(page.locator("main")).not.toContainText(/\bsettings\.[a-z_]/);

    // The payload factory pulls its own 19 kB namespace, not the agents one.
    dictChunks.length = 0;
    await page.goto("/payload-generator.html");
    await expect(page.locator("main").first()).toBeAttached({ timeout: 15_000 });
    await expect.poll(() => dictChunks.some((c) => c.includes("i18n-generate"))).toBe(true);
    await expect(page.locator("main")).not.toContainText(/\bgenerate\.[a-z_]/);

    // The AI page pulls its own 13 kB namespace.
    dictChunks.length = 0;
    await page.goto("/ai.html");
    await expect(page.locator("main").first()).toBeAttached({ timeout: 15_000 });
    await expect.poll(() => dictChunks.some((c) => c.includes("i18n-ai"))).toBe(true);
    await expect(page.locator("main")).not.toContainText(/\bai\.[a-z_]/);

    // The credentials page pulls its own 6 kB namespace.
    dictChunks.length = 0;
    await page.goto("/credentials.html");
    await expect(page.locator("main").first()).toBeAttached({ timeout: 15_000 });
    await expect.poll(() => dictChunks.some((c) => c.includes("i18n-cred"))).toBe(true);
    await expect(page.locator("main")).not.toContainText(/\bcred\.[a-z_]/);

    // The remaining single-page namespaces each have their own lazy chunk.
    const singlePage: Array<[string, string]> = [
      ["/bof", "i18n-bof"],
      ["/plugins", "i18n-plugins"],
      ["/phishing", "i18n-phishing"],
      ["/pivoting", "i18n-pivoting"],
      ["/toolkit", "i18n-toolkit"],
      ["/infrastructure", "i18n-infra"],
      ["/privilege-escalation", "i18n-privesc"],
      ["/lateral", "i18n-lateral"],
      ["/opsec", "i18n-opsec"],
      ["/ntlm", "i18n-ntlm"],
      ["/domain-fronting", "i18n-domain_fronting"],
      ["/scripting", "i18n-scripting"],
      ["/campaign", "i18n-campaign"],
      ["/topology", "i18n-topology"],
      ["/report", "i18n-report"],
      ["/automation", "i18n-auto"],
      ["/automation", "i18n-workflows"],
      ["/users", "i18n-users"],
      ["/listeners", "i18n-listeners"],
      ["/listeners", "i18n-dashboard"],
    ];
    for (const [path, chunk] of singlePage) {
      dictChunks.length = 0;
      await page.goto(`${path}.html`);
      await expect(page.locator("main").first()).toBeAttached({ timeout: 15_000 });
      await expect.poll(() => dictChunks.some((c) => c.includes(chunk)), { message: path }).toBe(true);
    }
    await expect(page.locator("main")).not.toContainText(/\btoolkit\.[a-z_]/);
  });

  test("the login page does not open the operator socket", async ({ page }) => {
    await prepare(page);
    const sockets: string[] = [];
    const meProbes: string[] = [];
    page.on("websocket", (socket) => sockets.push(socket.url()));
    page.on("request", (request) => {
      if (new URL(request.url()).pathname === "/api/me") meProbes.push(request.url());
    });

    await page.goto("/login.html");
    await expect(page.getByRole("form")).toBeVisible();
    // The provider used to connect on mount; give a retry time to fire.
    await page.waitForTimeout(1500);
    expect(sockets, "the signed-out login page opened a WebSocket").toHaveLength(0);
    expect(meProbes, "the signed-out login page probed /api/me").toHaveLength(0);

    // The authenticated shell still owns the socket and the identity probe.
    await page.goto("/dashboard.html");
    await expect(page.locator("main").first()).toBeAttached({ timeout: 15_000 });
    await expect.poll(() => sockets.length, { timeout: 10_000 }).toBeGreaterThan(0);
    await expect.poll(() => meProbes.length, { timeout: 10_000 }).toBeGreaterThan(0);
  });

  test("zh pages suspend until their translated dictionary fragment is ready", async ({ page }) => {
    await prepare(
      page,
      "light",
      ["credentials.read", "users.read", "automation.read", "campaigns.read"],
      "zh",
    );
    const zhChunks: string[] = [];
    page.on("request", (request) => {
      const pathname = new URL(request.url()).pathname;
      if (/\/assets\/(zh|i18n-zh-)[\w.-]+\.js$/.test(pathname)) zhChunks.push(pathname);
    });

    await page.goto("/beacons.html");
    await expect(page.locator("main").first()).toBeAttached({ timeout: 15_000 });
    await expect.poll(() => zhChunks.some((c) => c.includes("i18n-zh-agents"))).toBe(true);
    await expect.poll(() => zhChunks.some((c) => c.includes("i18n-zh-generate"))).toBe(true);
    await expect(page.locator("main")).toContainText(/[\u4e00-\u9fff]/, { timeout: 10_000 });
    const text = await page.locator("main").innerText();
    expect(text).not.toMatch(/\bagents\.[a-z_]/);

    // Each page pulls exactly the translated namespaces it renders.
    const pages: Array<[string, string]> = [
      ["/ai", "i18n-zh-ai"],
      ["/credentials", "i18n-zh-cred"],
      ["/payload-generator", "i18n-zh-generate"],
      ["/settings", "i18n-zh-settings"],
      ["/events", "i18n-zh-tasks"],
      ["/campaign", "i18n-zh-campaign"],
      ["/report", "i18n-zh-report"],
      ["/users", "i18n-zh-users"],
      ["/automation", "i18n-zh-auto"],
      ["/bof", "i18n-zh-bof"],
      ["/topology", "i18n-zh-topology"],
      ["/dashboard", "i18n-zh-dashboard"],
      ["/dashboard", "i18n-zh-listeners"],
    ];
    for (const [path, chunk] of pages) {
      zhChunks.length = 0;
      await page.goto(`${path}.html`);
      await expect(page.locator("main").first()).toBeAttached({ timeout: 15_000 });
      await expect.poll(() => zhChunks.some((c) => c.includes(chunk)), { message: path }).toBe(true);
      await expect(page.locator("main")).toContainText(/[\u4e00-\u9fff]/, { timeout: 10_000 });
    }
  });

  test("the command palette still opens after the lazy shell chunk loads", async ({ page }) => {
    await prepare(page);
    // The keydown listener lives in the lazy chunk; wait for the response and
    // let the effect install before pressing, otherwise the keypress is missed.
    const chunk = page.waitForResponse(/\/assets\/CommandPalette-[\w.-]+\.js$/, { timeout: 15_000 });
    await page.goto("/dashboard.html");
    await expect(page.locator("main").first()).toBeAttached({ timeout: 15_000 });
    await chunk;
    await page.waitForTimeout(400);
    await page.keyboard.press("Control+k");
    const palette = page.getByRole("dialog", { name: /command palette/i });
    await expect(palette).toBeVisible({ timeout: 10_000 });
    await page.keyboard.press("Escape");
    await expect(palette).toBeHidden();
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
        // The mocked socket is aborted, so the console reports Offline once
        // the fast-offline probe observes two health-ok failures. Waiting for
        // the banner under the header pins that state before the screenshot;
        // without it the banner raced the capture and the baselines flaked
        // by a few thousand pixels on whichever pages captured first.
        if (route !== "login") {
          await expect(page.getByText(/Connection lost|Real-time updates paused/)).toBeVisible({ timeout: 15_000 });
          await page.waitForTimeout(300);
        }
        await expect(page).toHaveScreenshot(`${route}-${theme}.png`, {
          animations: "disabled",
          fullPage: true,
          maxDiffPixelRatio: 0.015,
        });
      });
    }
  }
});
