
import { Link } from "react-router-dom";
import { useLocation } from "react-router-dom";
import { useWebSocket } from "@/lib/useWebSocket";
import { useEffect, useState, useCallback, memo } from "react";
import { useI18n } from "@/lib/i18n";
import { useAppStore, initStatsWSListener } from "@/lib/store";
import { useVisibleInterval } from "@/lib/hooks/useVisibleInterval";
import { POLL } from "@/lib/polling";
import type { DashboardStats } from "@/types/agent";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Tooltip, TooltipTrigger, TooltipContent } from "@/components/ui/tooltip";
import { Badge } from "@/components/ui/badge";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Sheet, SheetContent, SheetTitle } from "@/components/ui/sheet";
import { StatusDot } from "@/components/ui/status-dot";
import { Shield, ChevronDown, LayoutGrid, Settings, Search, X } from "lucide-react";
import { NAV_SECTIONS, sidebarNavSections, filterNavByPermissions } from "@/lib/navigation";
import { prefetchRoute } from "@/lib/route-prefetch";
import { can } from "@/lib/permissions";
import type { PermissionKey } from "@/lib/permission-keys";
import {
  defaultSidebarSections as defaultSections,
  mergeSidebarSections,
  SIDEBAR_SECTIONS_KEY,
  SIDEBAR_SECTIONS_LEGACY_KEY,
} from "@/lib/sidebar-sections";

let _savedSections: Record<string, boolean> | null = null;

function getSavedSections(): Record<string, boolean> {
  if (_savedSections) return _savedSections;
  try {
    let raw = localStorage.getItem(SIDEBAR_SECTIONS_KEY);
    if (!raw) {
      raw = localStorage.getItem(SIDEBAR_SECTIONS_LEGACY_KEY);
    }
    if (!raw) return { ...defaultSections };
    const saved = JSON.parse(raw) as Record<string, boolean>;
    const merged = mergeSidebarSections(saved, defaultSections);
    _savedSections = merged;
    try {
      localStorage.setItem(SIDEBAR_SECTIONS_KEY, JSON.stringify(merged));
    } catch { /* ignore */ }
    return merged;
  } catch { /* ignore */ }
  return { ...defaultSections };
}

function saveSectionState(state: Record<string, boolean>) {
  _savedSections = state;
  try { localStorage.setItem(SIDEBAR_SECTIONS_KEY, JSON.stringify(state)); }
  catch { /* ignore */ }
}

const SidebarLogo = memo(function SidebarLogo({
  collapsed,
  onToggle,
  expandLabel,
  collapseLabel,
}: {
  collapsed: boolean;
  onToggle: () => void;
  expandLabel: string;
  collapseLabel: string;
}) {
  return (
    <div className={`border-b border-border/70 flex items-center ${collapsed ? 'justify-center px-0 py-2' : 'px-3 py-2.5'}`}>
      <Button variant="ghost" onClick={onToggle}
        className={`flex items-center gap-x-2.5 hover:bg-muted/60 transition-colors duration-200 ${collapsed ? 'justify-center w-full' : ''}`}
        aria-label={collapsed ? expandLabel : collapseLabel}
        aria-expanded={!collapsed}>
        <div className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary shadow-none ring-1 ring-primary/20">
          <Shield className="size-4 text-primary-foreground" />
        </div>
        {!collapsed && (
          <div className="text-left leading-tight">
            <span className="font-bold text-xs tracking-tight text-foreground font-mono">Forge</span>
            <span className="font-bold text-xs tracking-tight text-muted-foreground font-mono">C2</span>
            <div className="mono-eyebrow text-muted-foreground/80">net · ops</div>
          </div>
        )}
      </Button>
    </div>
  );
});

const SidebarNav = memo(function SidebarNav({ collapsed, sections, toggleSection, pathname, stats, t, searchQuery, permissions, unreadNotifications }: {
  collapsed: boolean;
  sections: Record<string, boolean>;
  toggleSection: (key: string) => void;
  pathname: string;
  stats: DashboardStats | null;
  t: (key: string, params?: Record<string, string | number>) => string;
  searchQuery: string;
  permissions: readonly PermissionKey[] | null | undefined;
  unreadNotifications: number;
}) {
  function isActive(href: string) {
    if (href === "/dashboard") return pathname === "/dashboard";
    return pathname.startsWith(href);
  }

  const searching = searchQuery.trim().length > 0;
  const query = searching ? searchQuery.toLowerCase() : "";

  const filteredSections = searching
    ? sidebarNavSections().map((section) => ({
        ...section,
        items: filterNavByPermissions(section.items, permissions).filter((item) =>
          t(item.labelKey).toLowerCase().includes(query)
        ),
      })).filter((section) => section.items.length > 0)
    : sidebarNavSections().map((section) => ({
        ...section,
        items: filterNavByPermissions(section.items, permissions),
      })).filter((section) => section.items.length > 0);

  return (
    <nav className={collapsed ? 'flex flex-col items-center gap-1' : 'space-y-0.5 text-sm'}>
      {filteredSections.map((section, idx) => (
        <div key={section.titleKey} className={collapsed ? 'w-full' : ''}>
          {collapsed && idx > 0 && <div className="mx-auto my-1 h-px w-6 bg-border/70" aria-hidden="true" />}
          {!collapsed && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => { if (!section.pinned) toggleSection(section.titleKey); }}
              disabled={section.pinned}
              aria-expanded={section.pinned ? undefined : (searching ? true : !!sections[section.titleKey])}
              className={`w-full min-h-8 flex items-center gap-x-1.5 px-2 py-1 cursor-pointer select-none justify-start text-muted-foreground transition-colors hover:text-foreground aria-expanded:bg-transparent disabled:opacity-100 disabled:cursor-default ${idx > 0 ? 'mt-2 border-t-border/50 pt-2' : ''}`}
            >
              {!section.pinned && (
                <ChevronDown className={`size-3 text-muted-foreground transition-transform duration-200 ${(searching || sections[section.titleKey]) ? '' : '-rotate-90'}`} />
              )}
              <span className="mono-eyebrow text-muted-foreground/85">{t("section." + section.titleKey)}</span>
              {section.titleKey === "lab" && (
                <Badge variant="secondary" className="ml-auto px-1.5 py-px text-micro leading-none rounded bg-warning/15 text-warning-ink dark:bg-warning/10 font-medium">
                  {t("section.lab_badge")}
                </Badge>
              )}
            </Button>
          )}
          {(searching || section.pinned || sections[section.titleKey]) && section.items.map((item) => {
            const Icon = item.icon;
            const linkEl = (
              <Link
                key={item.href}
                to={item.href}
                onMouseEnter={() => prefetchRoute(item.href)}
                onFocus={() => prefetchRoute(item.href)}
                aria-current={isActive(item.href) ? "page" : undefined}
                aria-label={collapsed ? t(item.labelKey) : undefined}
                className={`flex items-center gap-x-2.5 rounded-lg transition-colors duration-150 ${collapsed ? 'group relative' : ''}
                  ${collapsed ? 'justify-center px-0 py-2 mx-auto size-10' : 'min-h-9 px-2 py-1.5'}
                  ${isActive(item.href)
                    ? 'nav-item-active bg-primary/10 font-medium text-primary'
                    : 'text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground'}`}
              >
                <span className="relative inline-flex">
                  <Icon className={collapsed ? 'size-5' : 'size-4'} />
                  {collapsed && item.badge === "agents" && stats != null && (stats.online_agents ?? 0) > 0 && (
                    <StatusDot tone="success" size="xs" className="absolute -top-1 -right-2" />
                  )}
                  {collapsed && item.badge === "listeners" && stats != null && (stats.total_listeners ?? 0) > 0 && (
                    <StatusDot tone="success" size="xs" className="absolute -top-1 -right-2" />
                  )}
                  {collapsed && item.badge === "notifications" && unreadNotifications > 0 && (
                    <StatusDot tone="warning" size="xs" className="absolute -top-1 -right-2" />
                  )}
                </span>
                {!collapsed && (
                  <>
                    <span className="truncate flex-1 text-xs">{t(item.labelKey)}</span>
                    {item.badge === "agents" && stats != null && (
                      <Badge variant="secondary" className="px-1.5 py-px text-micro leading-none rounded bg-primary/15 text-primary dark:bg-primary/10 font-mono">
                        {stats.online_agents ?? 0}
                      </Badge>
                    )}
                    {item.badge === "listeners" && stats != null && (
                      <Badge variant="secondary" className="px-1.5 py-px text-micro leading-none rounded bg-primary/10 text-primary font-mono">
                        {stats.total_listeners ?? 0}
                      </Badge>
                    )}
                    {item.badge === "notifications" && unreadNotifications > 0 && (
                      <Badge variant="secondary" className="px-1.5 py-px text-micro leading-none rounded bg-warning/10 text-warning-ink font-mono">
                        {unreadNotifications > 99 ? "99+" : unreadNotifications}
                      </Badge>
                    )}
                  </>
                )}
              </Link>
            );
            return collapsed ? (
              <Tooltip key={item.href}>
                <TooltipTrigger render={linkEl} />
                <TooltipContent side="right">{t(item.labelKey)}</TooltipContent>
              </Tooltip>
            ) : linkEl;
          })}
        </div>
      ))}
    </nav>
  );
});

const SidebarFooter = memo(function SidebarFooter({ collapsed, connected, onlineUsers, currentUsername, t, onOpenTools, permissions }: {
  collapsed: boolean;
  connected: boolean;
  onlineUsers: Array<{ username: string }>;
  currentUsername: string;
  t: (key: string, params?: Record<string, string | number>) => string;
  onOpenTools: () => void;
  permissions: readonly PermissionKey[] | null | undefined;
}) {
  return (
    <div className={`border-t border-border/60 ${collapsed ? 'px-2 py-2' : 'px-3 py-2.5'} space-y-2`}>
      {!collapsed && connected && (
        <div className="space-y-1">
          <div className="mono-eyebrow text-muted-foreground">{t("sidebar.online_operators")}</div>
          {currentUsername && (
              <div className="flex items-center gap-x-2 text-xs-sm text-foreground font-medium">
                <StatusDot tone="success" size="xs" pulse />
                <span className="truncate mono-cell">{t("sidebar.current_operator", { username: currentUsername })}</span>
              </div>
            )}
            {onlineUsers.filter((u) => u.username !== currentUsername).map((u) => (
              <div key={u.username} className="flex items-center gap-x-2 text-xs-sm text-muted-foreground">
                <StatusDot tone="success" size="xs" />
                <span className="truncate">{u.username}</span>
              </div>
            ))}
        </div>
      )}
      <div className={collapsed ? "grid gap-1" : "grid grid-cols-2 gap-1.5"}>
        <Button
          variant="ghost"
          size={collapsed ? "icon" : "sm"}
          onClick={onOpenTools}
          aria-label={t("sidebar.more_tools")}
          className={collapsed ? "mx-auto" : "justify-start"}
        >
          <LayoutGrid className="size-4" />
          {!collapsed && <span>{t("sidebar.more_tools")}</span>}
        </Button>
        {/*
          The footer Settings shortcut is not part of NAV_SECTIONS, so
          filterNavByPermissions never gated it: a custom read-only role without
          settings.read saw the button and got a 403 page. Mirror the nav rule
          here (unknown permissions while loading still show the button).
        */}
        {(!permissions || can(permissions, "settings.read")) && (
          <Button
            variant="ghost"
            size={collapsed ? "icon" : "sm"}
            render={<Link to="/settings" />}
            aria-label={t("nav.settings")}
            className={collapsed ? "mx-auto" : "justify-start"}
          >
            <Settings className="size-4" />
            {!collapsed && <span>{t("nav.settings")}</span>}
          </Button>
        )}
      </div>
    </div>
  );
});

export default function Sidebar() {
  const { pathname } = useLocation();
  const { connected, subscribe } = useWebSocket();
  const { t } = useI18n();
  const [sections, setSections] = useState<Record<string, boolean>>(defaultSections);
  const [searchQuery, setSearchQuery] = useState("");
const stats = useAppStore((s) => s.stats);
  const fetchStats = useAppStore((s) => s.fetchStats);
  const sidebarCollapsed = useAppStore((s) => s.sidebarCollapsed);
  const isMobile = useAppStore((s) => s.isMobile);
  const mobileMenuOpen = useAppStore((s) => s.mobileMenuOpen);
  const toggleSidebar = useAppStore((s) => s.toggleSidebar);
  const setMobileMenuOpen = useAppStore((s) => s.setMobileMenuOpen);
  const setCommandPaletteOpen = useAppStore((s) => s.setCommandPaletteOpen);
  const onlineUsers = useAppStore((s) => s.onlineUsers);
  const setOnlineUsers = useAppStore((s) => s.setOnlineUsers);
  const currentUsername = useAppStore((s) => s.currentUsername);
  const permissions = useAppStore((s) => s.currentPermissions);
  const unreadNotifications = useAppStore((s) => s.unreadNotifications);

  useEffect(() => { Promise.resolve().then(() => setSections(getSavedSections())); }, []);

  // Keep the active workstream visible when the route changes (Ctrl+K, deep
  // link, breadcrumb) instead of leaving its section collapsed.
  useEffect(() => {
    const active = NAV_SECTIONS.find((section) =>
      section.items.some((item) => pathname === item.href || pathname.startsWith(item.href + "/")),
    );
    if (!active) return;
    setSections((prev) => {
      if (prev[active.titleKey]) return prev;
      const next = { ...prev, [active.titleKey]: true };
      saveSectionState(next);
      return next;
    });
  }, [pathname]);

  const collapsed = sidebarCollapsed;

  useEffect(() => { fetchStats(); }, [fetchStats]);
  useEffect(() => { initStatsWSListener(); }, []);
  // WS pushes stats updates (agent_online/offline, task_update…); the tight
  // 30s poll only earns its keep while the socket is down.
  useVisibleInterval(fetchStats, connected ? POLL.wsDrift : POLL.stats);

  useEffect(() => {
    return subscribe((msg) => {
      const m = msg as { type?: string; online_users?: Array<{ username: string; connected_at: string }> };
      if (m.type === "user_online" || m.type === "user_offline") {
        if (m.online_users) setOnlineUsers(m.online_users);
      }
    });
  }, [subscribe, setOnlineUsers]);

  useEffect(() => {
    if (!mobileMenuOpen) return;
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        if (isMobile) setMobileMenuOpen(false);
        else toggleSidebar();
      }
    };
    window.addEventListener("keydown", handleKey);
    return () => window.removeEventListener("keydown", handleKey);
  }, [mobileMenuOpen, isMobile, toggleSidebar, setMobileMenuOpen]);

  const toggleSection = useCallback((key: string) => {
    setSections((prev) => {
      const next = { ...prev, [key]: !prev[key] };
      saveSectionState(next);
      return next;
    });
  }, []);

  const navContent = (
    <>
      <SidebarLogo
        collapsed={collapsed}
        onToggle={toggleSidebar}
        expandLabel={t("a11y.expand_sidebar")}
        collapseLabel={t("a11y.collapse_sidebar")}
      />
      {!collapsed && (
        <div className="relative px-2 pt-2">
          <Search aria-hidden="true" className="pointer-events-none absolute left-4 top-[0.95rem] size-3.5 text-muted-foreground" />
          <Input
            type="text"
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
            placeholder={t("common.search") + "..."}
            aria-label={t("common.search")}
            className="h-8 bg-secondary/50 pl-8 pr-8 text-xs-sm placeholder:text-muted-foreground"
          />
          {searchQuery && (
            <Button
              type="button"
              variant="ghost"
              size="icon"
              onClick={() => setSearchQuery("")}
              aria-label={t("common.clear")}
              className="absolute right-2 top-[0.625rem] size-6 text-muted-foreground hover:text-foreground"
            >
              <X className="size-3.5" />
            </Button>
          )}
        </div>
      )}
      <div className={`flex-1 overflow-hidden ${collapsed ? 'p-1' : 'p-2'}`}>
        <ScrollArea className="h-full">
          <SidebarNav
            collapsed={collapsed}
            sections={sections}
            toggleSection={toggleSection}
            pathname={pathname}
            stats={stats}
            t={t}
            searchQuery={searchQuery}
            permissions={permissions}
            unreadNotifications={unreadNotifications}
          />
        </ScrollArea>
      </div>
      <SidebarFooter
        collapsed={collapsed}
        connected={connected}
        onlineUsers={onlineUsers}
        currentUsername={currentUsername}
        t={t}
        onOpenTools={() => setCommandPaletteOpen(true)}
        permissions={permissions}
      />
    </>
  );

  // Mobile: use Sheet
  if (isMobile) {
    return (
      <Sheet open={mobileMenuOpen} onOpenChange={setMobileMenuOpen}>
        <SheetContent side="left" size="xs" showCloseButton={false}
          className="border-r border-border bg-sidebar/95 p-0 backdrop-blur-xl">
          <SheetTitle className="sr-only">{t("a11y.navigation")}</SheetTitle>
          <div className="flex flex-col h-full">
            {navContent}
          </div>
        </SheetContent>
      </Sheet>
    );
  }

  // Desktop: fixed aside
  return (
    <aside className={`flex h-screen flex-col overflow-hidden transition-all duration-200 ease-in-out supports-[height:100dvh]:h-[100dvh]
      bg-sidebar border-r border-border fixed left-0 top-0 z-40 shadow-sm
      ${collapsed ? 'w-[var(--shell-sidebar-collapsed)]' : 'w-[var(--shell-sidebar-expanded)]'}`}
    >
      {navContent}
    </aside>
  );
}
