import { lazy, Suspense, type ComponentType, type ReactNode } from "react";
import {
  createBrowserRouter,
  Outlet,
  Navigate,
  useLocation,
  type To,
} from "react-router-dom";
import { PageSpinner } from "@/components/ui/spinner";
import ErrorBoundary from "@/components/ErrorBoundary";
import AppLayout from "@/components/AppLayout";
import RouterErrorView from "@/components/RouterErrorView";
import { useCurrentUserBootstrap } from "@/components/ClientProvider";
import DeferredToaster from "@/components/DeferredToaster";
import { I18nProvider } from "@/lib/i18n";
import { useZhFragment } from "@/lib/i18n";
import { WebSocketProvider } from "@/lib/wsContext";
import NotFound from "@/pages/NotFound";
import Forbidden from "@/pages/forbidden/ForbiddenPage";
import { NAV_ITEMS } from "@/lib/navigation";
import { ROUTE_PAGE_IMPORTS } from "@/lib/route-imports";
import { useAppStore } from "@/lib/store";
import { canAny } from "@/lib/permissions";
import type { PermissionKey } from "@/lib/permission-keys";

const lazyPage = (imp: () => Promise<{ default: unknown }>) =>
  lazy(imp as () => Promise<{ default: ComponentType }>);

// One lazy chunk per page. The import map lives in src/lib/route-imports.ts so
// nav prefetch (src/lib/route-prefetch.ts) asks for the exact same chunks.
const MAIN_PAGES: Record<string, ReturnType<typeof lazyPage>> = Object.fromEntries(
  Object.entries(ROUTE_PAGE_IMPORTS).map(([name, load]) => [name, lazyPage(load)]),
);

// The login route is lazy like every other page. The sign-in form is the only
// first-paint route that pulled in zod (its validation schema), which kept the
// whole zod vendor chunk in the critical path for operators who never saw the
// form; splitting it drops that chunk from first paint. The 401 handler warms
// this chunk before redirecting (src/lib/login-preload.ts), so an expired
// session still lands on an instantly painting form.
const LoginPage = lazyPage(() => import("@/pages/login/LoginPage"));
const BeaconDetailPage = lazyPage(() => import("@/pages/beacons/detail/BeaconDetailPage"));
const BeaconConfigPage = lazyPage(() => import("@/pages/beacons/detail/config/ConfigPage"));
const BeaconFilesPage = lazyPage(() => import("@/pages/beacons/detail/files/FilesPage"));
const BeaconPersistencePage = lazyPage(() => import("@/pages/beacons/detail/persistence/PersistencePage"));
const BeaconRemoteDesktopPage = lazyPage(() => import("@/pages/beacons/detail/remote-desktop/RemoteDesktopPage"));
const BeaconScreenPage = lazyPage(() => import("@/pages/beacons/detail/screen/ScreenPage"));
const BeaconShellPage = lazyPage(() => import("@/pages/beacons/detail/shell/ShellPage"));
const BeaconTokenPage = lazyPage(() => import("@/pages/beacons/detail/token/TokenPage"));
const BeaconTrafficPage = lazyPage(() => import("@/pages/beacons/detail/traffic/BeaconTrafficPage"));
const ListenerDetailPage = lazyPage(() => import("@/pages/listeners/detail/ListenerDetailPage"));

type MainRoute =
  | { path: string; comp: ReturnType<typeof lazyPage> }
  | { path: string; redirect: string }
  | { path: string; legacyTo: string };

/**
 * U7 w2 renamed six sections (and the /agents detail family) to the Beacon
 * vocabulary. Every pre-rename URL stays addressable: the export still ships
 * its HTML shell, the router rewrites it client-side, and the query string
 * survives the hop (/agents?status=offline -> /beacons?status=offline), so
 * bookmarks, external links and muscle memory keep working.
 */
const LEGACY_REDIRECTS: { path: string; to: string }[] = [
  { path: "/agents", to: "/beacons" },
  { path: "/agents/:id", to: "/beacons" },
  { path: "/agents/:id/config", to: "/beacons" },
  { path: "/agents/:id/files", to: "/beacons" },
  { path: "/agents/:id/persistence", to: "/beacons" },
  { path: "/agents/:id/remote-desktop", to: "/beacons" },
  { path: "/agents/:id/screen", to: "/beacons" },
  { path: "/agents/:id/shell", to: "/beacons" },
  { path: "/agents/:id/token", to: "/beacons" },
  { path: "/agents/:id/traffic", to: "/beacons" },
  { path: "/timeline", to: "/events" },
  { path: "/generate", to: "/payload-generator" },
  { path: "/privesc", to: "/privilege-escalation" },
  { path: "/chain", to: "/proxy-chains" },
  { path: "/asset-organization", to: "/asset-management" },
];

const MAIN_ROUTES: MainRoute[] = [
  ...Object.keys(MAIN_PAGES).map((name) => ({ path: `/${name}`, comp: MAIN_PAGES[name] })),
  // Chat is now a mode of the AI workspace. Keep the old URL as a
  // compatibility redirect so bookmarks and external links continue to work
  // without shipping a second chat application bundle.
  { path: "/chat", redirect: "/ai" },
  ...LEGACY_REDIRECTS.map((r) => ({ path: r.path, legacyTo: r.to })),
  { path: "/beacons/:id", comp: BeaconDetailPage },
  { path: "/beacons/:id/config", comp: BeaconConfigPage },
  { path: "/beacons/:id/files", comp: BeaconFilesPage },
  { path: "/beacons/:id/persistence", comp: BeaconPersistencePage },
  { path: "/beacons/:id/remote-desktop", comp: BeaconRemoteDesktopPage },
  { path: "/beacons/:id/screen", comp: BeaconScreenPage },
  { path: "/beacons/:id/shell", comp: BeaconShellPage },
  { path: "/beacons/:id/token", comp: BeaconTokenPage },
  { path: "/beacons/:id/traffic", comp: BeaconTrafficPage },
  { path: "/listeners/:id", comp: ListenerDetailPage },
];

/** Route-local suspense: each lazy page shows the standard spinner while its
 *  chunk loads (replaces the single Root-level Suspense from the declarative
 *  router). */
/** zh dictionary fragments a route's page renders. Kept in one place so the
 *  page components do not each need a hook; a page not listed here keeps its
 *  zh keys in the base zh dictionary. At most two namespaces per page. */
function zhNamespacesFor(pathname: string): string[] {
  if (pathname === "/settings") return ["settings", "generate"];
  if (pathname.startsWith("/asset-management") || pathname === "/roles") return ["agents", "settings"];
  if (pathname === "/payload-generator") return ["generate", "listeners", "profiles"];
  if (pathname === "/listeners") return ["listeners", "generate", "dashboard"];
  if (pathname.startsWith("/listeners/")) return ["listeners"];
  if (pathname === "/ai" || pathname === "/macros") return ["ai"];
  if (pathname === "/credentials") return ["cred"];
  if (pathname === "/beacons") return ["agents", "generate"];
  if (pathname.startsWith("/beacons/")) {
    const rest = pathname.slice("/beacons/".length).split("/");
    if (rest.length >= 2) {
      const sub = rest[1];
      if (sub === "persistence") return ["agents", "generate"];
      if (["config", "files", "remote-desktop", "screen", "shell", "token", "traffic"].includes(sub)) {
        return ["agents"];
      }
    }
    return ["agents", "ai", "generate", "cred"];
  }
  if (pathname === "/dashboard") return ["dashboard", "listeners", "tasks"];
  if (pathname === "/events") return ["tasks", "ai"];
  if (pathname === "/campaign") return ["campaign"];
  if (pathname === "/phishing") return ["phishing", "campaign", "users"];
  if (pathname === "/automation") return ["auto", "workflows"];
  if (pathname === "/report") return ["report"];
  if (pathname === "/users") return ["users"];
  if (pathname === "/topology") return ["topology"];
  if (pathname === "/bof") return ["bof"];
  if (pathname === "/plugins") return ["plugins"];
  if (pathname === "/toolkit") return ["agents", "toolkit"];
  if (pathname === "/loot" || pathname === "/tags") return ["agents"];
  if (pathname === "/pivoting") return ["pivoting"];
  if (pathname === "/privilege-escalation") return ["privesc"];
  if (pathname === "/lateral") return ["lateral"];
  if (pathname === "/opsec") return ["opsec"];
  if (pathname === "/ntlm") return ["ntlm"];
  if (pathname === "/domain-fronting") return ["domain_fronting"];
  if (pathname === "/scripting") return ["scripting"];
  if (pathname === "/infrastructure") return ["infra"];
  return [];
}

function PageZhGate({ children }: { children: ReactNode }) {
  const { pathname } = useLocation();
  const namespaces = zhNamespacesFor(pathname);
  // Fixed hook count across routes: the gate is reused while navigating.
  useZhFragment(namespaces[0] ?? "");
  useZhFragment(namespaces[1] ?? "");
  useZhFragment(namespaces[2] ?? "");
  useZhFragment(namespaces[3] ?? "");
  return <>{children}</>;
}

function withSuspense(node: ReactNode): ReactNode {
  return (
    <Suspense fallback={<PageSpinner />}>
      <PageZhGate>{node}</PageZhGate>
    </Suspense>
  );
}

/** Compatibility redirect for a renamed section: swaps the first path segment
 *  for the new canonical one and keeps the id, sub-route, query and hash. */
function LegacyPrefixRedirect({ to }: { to: string }) {
  const { pathname, search, hash } = useLocation();
  const rest = pathname.replace(/^\/[^/]+/, "");
  return <Navigate to={{ pathname: `${to}${rest}`, search, hash }} replace />;
}

function MainLayout() {
  useCurrentUserBootstrap();
  return (
    <I18nProvider>
      {/* The socket belongs to the authenticated shell: mounting it at the root
          made the public login page open /ws (404 behind the demo gateway) and
          keep retrying + health-polling while the visitor typed credentials. */}
      <WebSocketProvider>
        <AppLayout>
          <Outlet />
        </AppLayout>
        <DeferredToaster />
      </WebSocketProvider>
    </I18nProvider>
  );
}

/** href -> any-of perms from the nav registry (longest prefix match wins). */
const ROUTE_PERMS: Record<string, PermissionKey[] | undefined> = Object.fromEntries(
  NAV_ITEMS.map((i) => [i.href, i.perms]),
);

function requiredPerms(pathname: string): PermissionKey[] | undefined {
  let best = "";
  for (const [href, perms] of Object.entries(ROUTE_PERMS)) {
    if (perms && pathname.startsWith(href) && href.length > best.length) best = href;
  }
  return best ? ROUTE_PERMS[best] : undefined;
}

/** Route-level permission gate. Fail-open while permissions are still
 *  loading — the backend remains authoritative for enforcement. */
function PermissionRoute({ children }: { children: React.ReactNode }) {
  const pathname = useLocation().pathname;
  const permissions = useAppStore((s) => s.currentPermissions);
  const perms = requiredPerms(pathname);
  if (!perms || permissions == null) return <>{children}</>;
  if (!canAny(permissions, perms)) return <Navigate to="/forbidden" replace />;
  return <>{children}</>;
}

function guard(name: string, Comp: ComponentType): ReactNode {
  return withSuspense(
    <RouteErrorBoundary routeKey={name}>
      <PermissionRoute key={name}>
        <Comp />
      </PermissionRoute>
    </RouteErrorBoundary>,
  );
}

/** Per-route crash isolation: a rendering error takes down only its own
 *  page (with retry), never the nav sidebar or sibling routes. resetKey on
 *  the live pathname also recovers same-pattern navigation
 *  (e.g. /beacons/1 -> /beacons/2). */
function RouteErrorBoundary({ routeKey, children }: { routeKey: string; children: ReactNode }) {
  const pathname = useLocation().pathname;
  return (
    <ErrorBoundary key={routeKey} resetKey={pathname}>
      {children}
    </ErrorBoundary>
  );
}

export const router = createBrowserRouter([
  {
    path: "/",
    // HomePage was a spinner + navigate("/dashboard") stub; the redirect is
    // expressed directly so no chunk loads before the destination.
    element: <Navigate to="/dashboard" replace />,
    errorElement: <RouterErrorView />,
  },
  {
    path: "/login",
    element: withSuspense(<LoginPage />),
    errorElement: <RouterErrorView />,
  },
  {
    path: "/forbidden",
    element: withSuspense(<Forbidden />),
    errorElement: <RouterErrorView />,
  },
  {
    element: <MainLayout />,
    errorElement: <RouterErrorView />,
    children: [
      // Single route table: path -> lazy page or redirect target. Adding a
      // page means adding one entry here (plus its lazy import above) —
      // paths, guards and redirects can no longer drift apart.
      ...MAIN_ROUTES.map((r) => ("legacyTo" in r
        ? {
            path: r.path,
            element: <LegacyPrefixRedirect to={r.legacyTo} />,
            errorElement: <RouterErrorView />,
          }
        : "redirect" in r
          ? {
              path: r.path,
              element: <Navigate to={r.redirect} replace />,
              errorElement: <RouterErrorView />,
            }
          : {
              path: r.path,
              element: guard(r.path, r.comp),
              errorElement: <RouterErrorView />,
            })),
    ],
  },
  {
    path: "*",
    element: withSuspense(<NotFound />),
    errorElement: <RouterErrorView />,
  },
]);

// ── Route transitions (UI/UX M1) ─────────────────────────────────────────
// Every router-driven navigation — <Link> clicks and navigate() calls, the
// redirect elements included — funnels through router.navigate(), so wrapping
// that single method opts the whole console into the View Transitions API
// instead of touching the ~90 call sites. The animation already lives in
// globals.css (fadeSlideOut/fadeSlideUp on ::view-transition-*(root)) beside
// its prefers-reduced-motion kill switch; we additionally skip the API
// entirely when the user prefers reduced motion (no snapshots are taken at
// all), and environments without document.startViewTransition (jsdom, older
// browsers) keep the instant swap — react-router feature-detects the same
// thing internally, but checking here keeps its warning quiet.
const rawNavigate: typeof router.navigate = router.navigate.bind(router);

function routeTransitionAllowed() {
  if (typeof document === "undefined" || typeof document.startViewTransition !== "function") {
    return false;
  }
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return true;
  }
  return !window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

router.navigate = ((to: To | null | number, options?: Parameters<typeof rawNavigate>[1]) => {
  if (typeof to === "number") return rawNavigate(to);
  if (to === null || !routeTransitionAllowed()) return rawNavigate(to, options);
  // `Link` forwards its own `viewTransition` prop, which is a present-but-
  // undefined key when unset — a plain spread would clobber the default, so
  // only an explicit boolean may override it.
  const viewTransition = options?.viewTransition ?? true;
  return rawNavigate(to, { ...options, viewTransition });
}) as typeof router.navigate;
