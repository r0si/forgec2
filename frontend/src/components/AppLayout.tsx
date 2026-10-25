
import { useEffect, useCallback, useRef } from "react";
import Sidebar from "@/components/Sidebar";
import TopBar from "@/components/TopBar";
import UpdateBanner from "@/components/UpdateBanner";
import { UpdateDialog } from "@/components/UpdateDialog";
import AgentStatusBanner from "@/components/AgentStatusBanner";
import ShortcutsHelp from "@/components/ShortcutsHelp";
import ScrollToTop from "@/components/ScrollToTop";
import CommandPalette from "@/components/CommandPalette";
import GlobalInteractDock from "@/components/GlobalInteractDock";
import TelemetryCollector from "@/components/TelemetryCollector";
import { useLocation } from "react-router-dom";
import { useAppStore, selectSidebarWidth } from "@/lib/store";
import { useI18n } from "@/lib/i18n";
import { getPageTitleKey } from "@/lib/navigation";
import { startIdleRoutePrefetch } from "@/lib/route-prefetch";
import { isFlushPath } from "@/lib/layout";
import { cn } from "@/lib/utils";
import { useWS } from "@/lib/wsContext";
import { PageSpinner } from "@/components/ui/spinner";

export default function AppLayout({ children }: { children: React.ReactNode }) {
  const { pathname } = useLocation();
  const { t, ready } = useI18n();
  const isMobile = useAppStore((s) => s.isMobile);
  const setIsMobile = useAppStore((s) => s.setIsMobile);
  const setMobileMenuOpen = useAppStore((s) => s.setMobileMenuOpen);
  const focusMode = useAppStore((s) => s.focusMode);
  const { reconnectFailed } = useWS();

  const sidebarWidth = useAppStore(selectSidebarWidth);
  const flush = isFlushPath(pathname);

  const prevWidthRef = useRef<number>(typeof window !== "undefined" ? window.innerWidth : 1920);

  useEffect(() => {
    const check = () => {
      const w = window.innerWidth;
      const mobile = w < 1024;
      setIsMobile(mobile);
      // Entering the 1024–1280 compact band: default-collapse the sidebar so
      // narrow laptops get full content width. Leaving the band never fights
      // the operator's explicit toggle (only the crossing triggers it).
      if (!mobile && w < 1280 && prevWidthRef.current >= 1280) {
        useAppStore.setState({ sidebarCollapsed: true });
      }
      prevWidthRef.current = w;
    };
    check();
    window.addEventListener("resize", check);
    return () => window.removeEventListener("resize", check);
  }, [setIsMobile]);

  useEffect(() => {
    if (isMobile) useAppStore.setState({ sidebarCollapsed: true });
  }, [isMobile]);

  // G10 fix: skip e.repeat so holding Ctrl+. does not rapid-toggle focus mode.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.repeat) return;
      if ((e.metaKey || e.ctrlKey) && e.key === ".") {
        e.preventDefault();
        useAppStore.getState().toggleFocusMode();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  useEffect(() => {
    setMobileMenuOpen(false);
  }, [pathname, setMobileMenuOpen]);

  // Speculatively warm the pages operators open first, once the browser is
  // idle (sidebar hover/focus covers the rest). No-op on save-data links.
  useEffect(() => startIdleRoutePrefetch(), []);

  // Per-page document.title derived from the route's nav label.
  useEffect(() => {
    if (!pathname || pathname === "/login") return;
    const titleKey = getPageTitleKey(pathname);
    if (!titleKey) return;
    document.title = `${t(titleKey)} — ForgeC2`;
  }, [pathname, t]);

  const handleMenuToggle = useCallback(() => {
    if (isMobile) setMobileMenuOpen(true);
    else useAppStore.getState().toggleSidebar();
  }, [isMobile, setMobileMenuOpen]);

  return (
    <div className="flex h-screen overflow-hidden bg-background supports-[height:100dvh]:h-[100dvh]">
      {/* Skip to content link (WCAG 2.4.1) */}
      <a
        href="#main-content"
        className="sr-only focus:not-sr-only focus:fixed focus:top-2 focus:left-2 focus:z-(--z-skip-link) focus:bg-primary focus:text-primary-foreground focus:px-4 focus:py-2 focus:rounded-lg focus:text-sm focus:font-medium focus:shadow-lg focus:outline-none"
      >
        {t("a11y.skip_to_content")}
      </a>

      {!focusMode && <Sidebar />}

      <div
        className="flex flex-col flex-1 min-w-0 min-h-0 transition-[margin] duration-200 ease-in-out"
        style={{ marginLeft: focusMode || isMobile ? 0 : sidebarWidth }}
      >
        <TopBar onMenuToggle={handleMenuToggle} />
        <main
          id="main-content"
          tabIndex={-1}
          className={cn(
            "relative flex min-h-0 flex-1 flex-col bg-background focus:outline-none",
            reconnectFailed
              ? "pt-[calc(var(--shell-topbar-height)+2.5rem)]"
              : "pt-(--shell-topbar-height)",
            flush ? "overflow-hidden" : "overflow-y-auto scroll-smooth",
          )}
        >
          <div
            className={cn(
              "mx-auto flex w-full min-h-0 flex-col",
              flush
                ? "max-w-none flex-1 px-0 py-0"
                // The shell owns the page padding; the content width belongs to
                // PageContainer (standard / wide / workspace). Two nested
                // max-widths used to clamp the same value twice.
                : "gap-(--page-gap) px-(--page-pad-x) py-(--page-pad-y)",
            )}
          >
            {!flush && (
              <>
                <UpdateBanner />
                <UpdateDialog />
                <AgentStatusBanner />
              </>
            )}
            <div className={cn(flush && "flex min-h-0 flex-1 flex-col")}>
              {/* The full en dictionary is a separate chunk in production; hold
                  the routed page until it arrives so no raw key can flash. The
                  shell above already rendered from the synchronous bootstrap
                  slice. */}
              {ready ? children : <PageSpinner />}
            </div>
          </div>
        </main>
        <GlobalInteractDock />
      </div>

      <ShortcutsHelp />
      <ScrollToTop />
      <CommandPalette />
      <TelemetryCollector />
    </div>
  );
}
