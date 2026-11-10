
import { useWebSocket } from "@/lib/useWebSocket";
import { useI18n } from "@/lib/i18n";
import { useAppStore, selectSidebarWidth } from "@/lib/store";
import { ShortcutsHelpButton } from "@/components/ShortcutsHelp";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { ConnectionDot } from "@/components/ui/connection-dot";
import { ThemeSelector } from "@/components/topbar/theme-selector";
import { LanguageSelector } from "@/components/topbar/language-selector";
import { NotificationDropdown } from "@/components/topbar/notification-dropdown";
import { UserDropdown } from "@/components/topbar/user-dropdown";
import {
  Menu, AlertTriangle, Maximize2, Minimize2, Search,
} from "lucide-react";

export default function TopBar({ onMenuToggle }: { onMenuToggle?: () => void }) {
  const { t } = useI18n();
  const { connected, reconnectFailed, reconnect, demoMode } = useWebSocket();
  const storeSidebarWidth = useAppStore(selectSidebarWidth);
  const focusMode = useAppStore((s) => s.focusMode);
  const toggleFocusMode = useAppStore((s) => s.toggleFocusMode);
  const setCommandPaletteOpen = useAppStore((s) => s.setCommandPaletteOpen);
  const stats = useAppStore((s) => s.stats);

  return (
    <>
    <header
      className="fixed top-0 right-0 z-30 flex h-(--shell-topbar-height) items-center justify-between border-b border-border/80 bg-card/95 px-3 shadow-sm backdrop-blur-xl transition-[left] duration-200 ease-in-out sm:px-4"
      style={{ left: focusMode ? 0 : storeSidebarWidth }}
    >
      <div className="flex items-center gap-3 flex-1 min-w-0">
        <Button onClick={onMenuToggle}
          variant="ghost" size="icon" className="lg:hidden" aria-label={t("common.toggle_menu")}>
          <Menu className="size-5" />
        </Button>
        <Button
          type="button"
          variant="outline"
          onClick={() => setCommandPaletteOpen(true)}
          className="hidden h-9 min-w-0 max-w-sm flex-1 justify-start gap-2 bg-muted/45 px-3 text-muted-foreground shadow-none hover:border-primary/25 hover:bg-muted lg:flex"
          aria-label={t("palette.placeholder")}
        >
          <Search className="size-4 shrink-0" />
          <span className="truncate text-compact">{t("palette.placeholder")}</span>
          <kbd className="ml-auto shrink-0 rounded-md border border-border/80 bg-card px-1.5 py-0.5 font-mono text-micro-sm text-muted-foreground">Ctrl K</kbd>
        </Button>
        {/* The palette trigger is a full search field from lg up and a bare
            icon below it. Without the icon variant, phones and tablets had no
            way to reach navigation at all: the sidebar is a closed sheet and
            Ctrl+K needs a hardware keyboard. */}
        <Button
          type="button"
          variant="ghost"
          size="icon"
          onClick={() => setCommandPaletteOpen(true)}
          className="lg:hidden"
          aria-label={t("palette.placeholder")}
        >
          <Search className="size-5" />
        </Button>
      </div>

      <div className="flex items-center gap-1">
        {/* WS + Live Agents. The label is always rendered (it used to be
            `hidden lg:inline`, leaving a bare colour dot on phones, which
            fails WCAG 1.4.1 use-of-colour) and the pill reserves a minimum
            width so switching Live/Reconnecting/Offline cannot shift the row. */}
        <div className="mr-1 flex min-w-0 shrink-0 items-center gap-2 rounded-lg border border-border/70 bg-secondary/70 px-2.5 py-1 shadow-sm sm:mr-2">
          <Tooltip>
            <TooltipTrigger render={<button type="button" className="flex min-h-8 min-w-0 items-center gap-2 rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring/60" />}>
              <span role="status" aria-live="polite" className="flex min-w-0 items-center gap-2">
                {demoMode ? (
                  <span className="min-w-16 truncate text-micro-sm text-muted-foreground">{t("topbar.demo_mode")}</span>
                ) : (
                  <>
                    <ConnectionDot connected={connected} reconnectFailed={reconnectFailed} />
                    <span className="min-w-16 truncate text-micro-sm text-muted-foreground">{connected ? t("common.live") : reconnectFailed ? t("common.offline") : t("topbar.reconnecting")}</span>
                    {stats && (stats.online_agents ?? 0) > 0 && (
                      <span className="hidden xl:inline-flex items-center gap-1 text-micro-sm font-mono text-primary bg-primary/10 px-1.5 py-0.5 rounded-full">
                        {stats.online_agents} {t("topbar.online_label")}
                      </span>
                    )}
                  </>
                )}
              </span>
            </TooltipTrigger>
            <TooltipContent>
              {demoMode
                ? t("topbar.demo_mode_hint")
                : `${connected ? t("topbar.ws_connected") : reconnectFailed ? t("topbar.ws_lost") : t("topbar.reconnecting")}${stats ? ` · ${stats.online_agents ?? 0} ${t("nav.online_label")}` : ""}`}
            </TooltipContent>
          </Tooltip>
        </div>

        <span className="hidden md:inline-flex">
          <Tooltip>
            <TooltipTrigger render={<Button variant="ghost" size="icon" onClick={toggleFocusMode} aria-label={focusMode ? t("topbar.focus_mode_exit") : t("topbar.focus_mode")} />}>
              {focusMode ? <Minimize2 className="size-5" /> : <Maximize2 className="size-5" />}
            </TooltipTrigger>
            <TooltipContent>{focusMode ? t("topbar.focus_mode_exit") : t("topbar.focus_mode")}</TooltipContent>
          </Tooltip>
        </span>
        <span className="hidden md:inline-flex">
          <ShortcutsHelpButton />
        </span>
        <span className="hidden sm:inline-flex"><ThemeSelector /></span>
        <span className="hidden md:inline-flex"><LanguageSelector /></span>
        <NotificationDropdown />
        <UserDropdown />
      </div>
    </header>
    {reconnectFailed && (
      <div
        className="fixed top-(--shell-topbar-height) right-0 z-30 flex h-10 items-center gap-2 border-b border-destructive/30 bg-destructive/10 px-3 text-xs text-destructive-ink backdrop-blur-sm sm:px-4 sm:text-sm"
        style={{ left: focusMode ? 0 : storeSidebarWidth }}
      >
        <AlertTriangle className="size-4 shrink-0" />
        <span className="min-w-0 flex-1 truncate">{t("topbar.ws_disconnected_banner")}</span>
        <Button variant="outline" size="sm" className="ml-auto h-7 text-xs border-destructive/30 text-destructive-ink hover:bg-destructive/10" onClick={reconnect}>
          {t("topbar.reconnect")}
        </Button>
      </div>
    )}
    </>
  );
}
