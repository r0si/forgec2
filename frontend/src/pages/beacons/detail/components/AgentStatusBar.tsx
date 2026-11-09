
import { memo, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { StatusBadge } from "@/components/ui/status-indicator";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useI18n } from "@/lib/i18n";
import type { AgentDetail, AgentStatus } from "@/types/agent";
import { ArrowUp, FolderOpen, Monitor, Terminal } from "lucide-react";

interface AgentStatusBarProps {
  agent: Partial<AgentDetail>;
  agentId: string;
  status: AgentStatus;
}

/**
 * Compact sticky status strip shown once the page header scrolls out of
 * view. Sticks below the app topbar + breadcrumb dock (top-14 + dock).
 * Shares the page scroll container, so it aligns with the content column.
 */
export default memo(function AgentStatusBar({ agent, agentId, status }: AgentStatusBarProps) {
  const { t } = useI18n();
  const [visible, setVisible] = useState(false);
  const sentinelRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const el = sentinelRef.current;
    if (!el) return;
    const obs = new IntersectionObserver(
      (entries) => {
        setVisible(!entries[0]?.isIntersecting);
      },
      { threshold: 0 },
    );
    obs.observe(el);
    return () => obs.disconnect();
  }, []);

  const scrollToTop = () => {
    const scroller = document.querySelector("main#main-content");
    if (scroller) scroller.scrollTo({ top: 0, behavior: "smooth" });
    else window.scrollTo({ top: 0, behavior: "smooth" });
  };

  return (
    <>
      <div ref={sentinelRef} className="absolute top-0 h-px w-px" aria-hidden="true" />
      {visible && (
        <div className="sticky top-[96px] z-30 mb-4 animate-fade-slide-up">
          <div className="flex items-center gap-3 rounded-xl border border-border/70 bg-card/90 px-3.5 py-2 shadow-md backdrop-blur-xl">
            <h2 className="min-w-0 truncate text-sm font-semibold text-foreground">{agent.hostname || "\u2014"}</h2>
            <StatusBadge status={status} pulse={status === "online"} />
            <div className="ml-auto flex items-center gap-1.5 shrink-0">
              <Tooltip>
                <TooltipTrigger>
                  <Button variant="outline" size="sm" render={<Link to={`/beacons/${agentId}/shell`} />} className="h-7 gap-1.5 text-xs"><Terminal className="size-3.5" /> {t("nav.beacon_shell_title")}</Button>
                </TooltipTrigger>
                <TooltipContent>{t("agents.shortcut_hint", { key: "S" })}</TooltipContent>
              </Tooltip>
              <Tooltip>
                <TooltipTrigger>
                  <Button variant="outline" size="sm" render={<Link to={`/beacons/${agentId}/files`} />} className="h-7 gap-1.5 text-xs"><FolderOpen className="size-3.5" /> {t("agents.files_title")}</Button>
                </TooltipTrigger>
                <TooltipContent>{t("agents.shortcut_hint", { key: "F" })}</TooltipContent>
              </Tooltip>
              <Tooltip>
                <TooltipTrigger>
                  <Button variant="outline" size="sm" render={<Link to={`/beacons/${agentId}/screen`} />} className="h-7 gap-1.5 text-xs"><Monitor className="size-3.5" /> {t("nav.beacon_screen")}</Button>
                </TooltipTrigger>
                <TooltipContent>{t("agents.shortcut_hint", { key: "D" })}</TooltipContent>
              </Tooltip>
              <Button variant="ghost" size="sm" onClick={scrollToTop} className="h-7 px-2 text-xs text-muted-foreground" aria-label={t("agents.back_to_top")}>
                <ArrowUp className="size-3.5" />
              </Button>
            </div>
          </div>
        </div>
      )}
    </>
  );
})