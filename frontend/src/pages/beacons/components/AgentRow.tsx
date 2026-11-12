
import { memo } from "react";
import { Button } from "@/components/ui/button";
import { StatusBadge } from "@/components/ui/status-indicator";
import { Tooltip, TooltipTrigger, TooltipContent } from "@/components/ui/tooltip";
import { timeAgo, formatTime } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import type { Beacon } from "./types";
import { agentStatusBorderClass, osIcon, integrityTone } from "./types";
import { Checkbox } from "@/components/ui/checkbox";
import { Badge } from "@/components/ui/badge";
import { TableCell } from "@/components/ui/table";
import { Check, Copy, FolderOpen, HelpCircle, Link as LinkIcon, Lock, Maximize2, Monitor, MoreHorizontal, Shield, StickyNote, Terminal, Unlock, Users } from "lucide-react";
import type { AgentMenuPoint } from "./agent-menu-actions";
import { knownImplantVersion } from "@/lib/implant-version";
import { useCopiedField } from "@/lib/hooks/useCopiedField";

interface AgentRowProps {
  beacon: Beacon;
  isSelected: boolean;
  onToggleSelect: (id: string, checked: boolean) => void;
  onInteract: (id: string) => void;
  onDetails: (id: string) => void;
  onMenu: (point: AgentMenuPoint) => void;
  onEditNotes?: (beacon: Beacon) => void;
  onQuickNav?: (beacon: Beacon, view: "shell" | "files" | "screen") => void;
  taskCount: number;
  lockUser: string | null;
  locksUnread?: boolean;
  presenceUsers: string[] | null;
  visibleCols: Record<string, boolean>;
  tags?: { id: string; name: string; color: string }[];
}

export const AgentRow = memo(function AgentRow({
  beacon,
  isSelected,
  onToggleSelect,
  onInteract,
  onDetails,
  onMenu,
  onEditNotes,
  onQuickNav,
  taskCount,
  lockUser,
  locksUnread,
  presenceUsers,
  visibleCols,
  tags,
}: AgentRowProps) {
  const id = beacon.id || "";
  const { t } = useI18n();
  const { copiedField, copy } = useCopiedField();
  const hostname = beacon.hostname || "-";
  const username = beacon.username || "-";
  const ip = beacon.ip || "-";
  const os = beacon.os || "";
  const arch = beacon.arch || "";
  const status = beacon.status || "offline";
  const failures = Number(beacon.failures_24h ?? 0) || 0;
  const lastSeen = beacon.last_seen || "";
  const integrity = beacon.integrity || "";
  const elevated = beacon.elevated || false;
  const notes = beacon.notes || "";
  const activeWindow = beacon.active_window || "";
  const version = knownImplantVersion(beacon.version);

  const borderLeft = agentStatusBorderClass(status);
  const OsIcon = osIcon(os);

  return (
    <tr
      tabIndex={0}
      data-agent-id={id}
      onDoubleClick={() => onDetails(id)}
      onContextMenu={(e) => {
        e.preventDefault();
        onMenu({ x: e.clientX, y: e.clientY, beacon });
      }}
      onKeyDown={(e) => {
        if (e.target !== e.currentTarget) return;
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onInteract(id);
        }
      }}
      className={`group cursor-default hover:bg-secondary/60 hover:shadow-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset ${borderLeft} ${isSelected ? "bg-primary/5" : ""}`}
    >
      <TableCell className="py-1 px-2">
        <Checkbox aria-label={t("common.select_item")} name={`select-${id}`}
          checked={isSelected}
          onCheckedChange={(v) => onToggleSelect(id, v === true)}
          onClick={(e) => e.stopPropagation()}
        />
      </TableCell>
      {visibleCols.hostname && (
      <TableCell className="py-1 px-2">
        <div className="flex min-w-0 max-w-[180px] items-center gap-1 sm:max-w-[240px]">
          <Button
            variant="link"
            size="sm"
            onClick={(e) => { e.stopPropagation(); onInteract(id); }}
            title={hostname}
            className="h-auto min-w-0 flex-1 justify-start p-0 text-left font-mono text-xs text-primary hover:underline"
          >
            <span className="truncate">{hostname}</span>
          </Button>
          <span className="flex shrink-0 items-center">
            <Button
              variant="ghost"
              size="icon-xs"
              onClick={(e) => { e.stopPropagation(); copy(hostname, `host-${id}`); }}
              className="ml-1 opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 transition-opacity"
              aria-label={t("agents.copy_hostname")}
            >
              {copiedField === `host-${id}` ? <Check className="size-3 text-success-ink" /> : <Copy className="size-3" />}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              className={`ml-1 ${notes ? "" : "opacity-0 group-hover:opacity-100"}`}
              title={notes || t("agents.edit_notes")}
              aria-label={t("agents.edit_notes")}
              onClick={(e) => {
                e.stopPropagation();
                onEditNotes?.(beacon);
              }}
            >
              <StickyNote className={`size-3.5 ${notes ? "text-primary" : "text-muted-foreground"}`} />
            </Button>
            {beacon.parent_id && (
              <span className="ml-1 text-micro-sm text-chart-6" title={t("agents.p2p_chained")}>
                <LinkIcon className="size-4" />
              </span>
            )}
          </span>
        </div>
      </TableCell>
      )}
      {visibleCols.username && (
      <TableCell className="py-1 px-2 text-xs">
        <span className="block max-w-[120px] truncate font-mono text-muted-foreground sm:max-w-[160px]" title={username}>{username}</span>
      </TableCell>
      )}
      {visibleCols.os && (
      <TableCell className="py-1 px-2">
        <div className="flex items-center gap-1.5 flex-wrap">
          <Badge variant="secondary" className="text-xs-sm gap-1.5 whitespace-nowrap bg-secondary/60">
            <OsIcon className="size-3 text-muted-foreground" />
            {os}{arch ? ` ${arch}` : ""}
          </Badge>
          {integrity && (
            <Badge variant={integrityTone(integrity)} className="text-micro-sm font-medium">{integrity}</Badge>
          )}
          {elevated && <Badge variant="destructive" className="text-micro-sm font-bold" title={t("agents.elevated")}><Shield className="size-3" /></Badge>}
        </div>
      </TableCell>
      )}
      {visibleCols.ip && (
      <TableCell className="py-1 px-2">
        <span className="font-mono text-xs text-muted-foreground">{ip}</span>
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={(e) => { e.stopPropagation(); copy(ip, `ip-${id}`); }}
          className="ml-1 opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 transition-opacity"
          aria-label={t("agents.copy_ip")}
        >
          {copiedField === `ip-${id}` ? <Check className="size-3 text-success-ink" /> : <Copy className="size-3" />}
        </Button>
      </TableCell>
      )}
      {visibleCols.last_seen && (
      <TableCell className="py-1 px-2 text-xs text-muted-foreground font-mono whitespace-nowrap">
        <span title={lastSeen ? formatTime(lastSeen) : ""}>{timeAgo(lastSeen, t)}</span>
      </TableCell>
      )}
      {visibleCols.window && (
      <TableCell className="py-1 px-2 text-xs text-foreground max-w-[140px] max-sm:hidden">
        {activeWindow ? (
          <span className="inline-flex items-center gap-1 truncate" title={activeWindow}>
            <Maximize2 className="size-4" />
            <span className="truncate">{activeWindow}</span>
          </span>
        ) : <span className="text-muted-foreground dark:text-muted-foreground"></span>}
      </TableCell>
      )}
      {visibleCols.lock && (
      <TableCell className="py-1 px-2 text-center max-sm:hidden">
        {lockUser ? (
          <span className="inline-flex items-center gap-1 text-xs" title={t("agents.locked_by").replace("{user}", lockUser)}>
            <Lock className="size-4" />
            <span className="text-micro-sm text-warning-ink font-medium truncate max-w-[80px]">{lockUser}</span>
          </span>
        ) : locksUnread ? (
          // The lock table has never been read successfully. Showing the
          // unlock glyph here would assert "not locked" for every agent, which
          // is exactly the claim an operator must not be given blind.
          <span className="text-muted-foreground text-xs-sm" title={t("agents.lock_state_unreadable")}>
            <HelpCircle className="size-4" />
          </span>
        ) : (
          <span className="text-muted-foreground text-xs-sm">
            <Unlock className="size-4" />
          </span>
        )}
        {presenceUsers && presenceUsers.length > 0 && (
          <span className="inline-flex items-center gap-1 text-xs ml-1" title={t("agents.active_operator").replace("{user}", presenceUsers.join(", "))}>
            <Users className="size-3.5 text-info-ink" />
            <span className="text-micro-sm text-info-ink truncate max-w-[80px]">{presenceUsers.length}</span>
          </span>
        )}
      </TableCell>
      )}
      {visibleCols.tasks && (
      <TableCell className="py-1 px-2 text-center max-sm:hidden">
        <Tooltip>
          <TooltipTrigger render={<span className="text-xs font-mono text-foreground cursor-default">{taskCount}</span>} />
          <TooltipContent side="top">
            {beacon.taskStats ? (
              <div className="space-y-0.5">
                {beacon.taskStats.pending > 0 && <div>{t("agents.task_pending")}: {beacon.taskStats.pending}</div>}
                {beacon.taskStats.running > 0 && <div>{t("agents.task_running")}: {beacon.taskStats.running}</div>}
                <div>{t("agents.task_completed")}: {beacon.taskStats.completed}</div>
                {beacon.taskStats.failed > 0 && <div>{t("agents.task_failed")}: {beacon.taskStats.failed}</div>}
              </div>
            ) : (
              <span>{t("agents.total_tasks").replace("{n}", String(taskCount))}</span>
            )}
          </TooltipContent>
        </Tooltip>
      </TableCell>
      )}
      {visibleCols.version && (
      <TableCell className="py-1 px-2 text-xs font-mono text-muted-foreground max-sm:hidden">
        {version ? (
          version
        ) : (
          <span className="text-warning-ink" title={t("agents.version_unknown_hint")}>{t("agents.version_unknown")}</span>
        )}
        {beacon.protocol_outdated && (
          <Tooltip>
            <TooltipTrigger render={
              <Badge variant="warning" className="ml-1.5 font-mono">
                PV{beacon.protocol_version}
              </Badge>
            } />
            <TooltipContent>
              {t("agents.protocol_outdated_hint", {
                version: beacon.protocol_version ?? 0,
                current: beacon.protocol_current ?? 0,
              })}
            </TooltipContent>
          </Tooltip>
        )}
      </TableCell>
      )}
      {visibleCols.sleep && (
      <TableCell className="py-1 px-2 text-xs font-mono text-muted-foreground text-right max-sm:hidden">
        {typeof beacon.current_interval === "number" && beacon.current_interval > 0
          ? `${beacon.current_interval}s`
          : "-"}
      </TableCell>
      )}
      {visibleCols.jitter && (
      <TableCell className="py-1 px-2 text-xs font-mono text-muted-foreground text-right max-sm:hidden">
        {typeof beacon.current_jitter === "number" && beacon.current_jitter >= 0
          ? `${beacon.current_jitter}%`
          : "-"}
      </TableCell>
      )}
      {visibleCols.status && (
      <TableCell className="py-1 px-2">
        <StatusBadge status={status} variant="dot" size="sm" pulse={status === "online"} />
      </TableCell>
      )}
      {visibleCols.failures && (
      <TableCell className="py-1 px-2 text-center">
        {/* The failed operations of the last 24 hours. Zero is not a warning,
            but a beacon that fails and fails is the one the operator has to
            look at - so the count is the column's whole point. */}
        {failures > 0 ? (
          <Badge variant="destructive" className="font-mono">{failures}</Badge>
        ) : (
          <span className="text-xs text-muted-foreground">0</span>
        )}
      </TableCell>
      )}
      <TableCell className="sticky-end py-1 px-2 text-right">
        <div className="flex items-center justify-end gap-0.5">
          {tags && tags.length > 0 && (
            <span className="hidden xl:inline-flex items-center gap-1 mr-1">
              {tags.slice(0, 3).map((tag) => (
                <span key={tag.id} className="size-2 rounded-full ring-1 ring-white dark:ring-border shadow-sm" style={{ backgroundColor: tag.color }} title={tag.name} />
              ))}
              {tags.length > 3 && <span className="text-micro text-muted-foreground">+{tags.length - 3}</span>}
            </span>
          )}
          <Button
            variant="ghost"
            size="icon-xs"
            className="opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 transition-opacity"
            aria-label={t("nav.beacon_shell_title")}
            onClick={(e) => { e.stopPropagation(); onQuickNav?.(beacon, "shell"); }}
          >
            <Terminal className="size-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon-xs"
            className="opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 transition-opacity"
            aria-label={t("agents.files_title")}
            onClick={(e) => { e.stopPropagation(); onQuickNav?.(beacon, "files"); }}
          >
            <FolderOpen className="size-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon-xs"
            className="opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 transition-opacity"
            aria-label={t("nav.beacon_screen")}
            onClick={(e) => { e.stopPropagation(); onQuickNav?.(beacon, "screen"); }}
          >
            <Monitor className="size-4" />
          </Button>
          <Tooltip>
          <TooltipTrigger render={
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={t("agents.context_menu")}
              onClick={(e) => {
                e.stopPropagation();
                const rect = e.currentTarget.getBoundingClientRect();
                onMenu({ x: rect.right, y: rect.bottom, beacon });
              }}
            >
              <MoreHorizontal className="size-4" />
            </Button>
          } />
          <TooltipContent side="top">{t("agents.context_menu")}</TooltipContent>
        </Tooltip>
        </div>
      </TableCell>
    </tr>
  );
});
