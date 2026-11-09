
import { useEffect, useRef } from "react";
import { createPortal } from "react-dom";
import { useI18n } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import {
  Camera,
  Clock,
  Copy,
  FolderOpen,
  IdCard,
  Network,
  Monitor,
  MoreHorizontal,
  Radio,
  RefreshCw,
  StickyNote,
  Terminal,
  Trash2,
  Skull,
} from "lucide-react";
import type { AgentMenuAction, AgentMenuPoint } from "./agent-menu-actions";
import { clampMenuPoint } from "./agent-menu-actions";

interface AgentContextMenuProps {
  point: AgentMenuPoint | null;
  onClose: () => void;
  onAction: (action: AgentMenuAction) => void;
}

export function AgentContextMenu({ point, onClose, onAction }: AgentContextMenuProps) {
  const { t } = useI18n();

  const containerRef = useRef<HTMLDivElement>(null);
  const prevFocus = useRef<HTMLElement | null>(null);
  const prevPoint = useRef<AgentMenuPoint | null>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    if (!point) {
      prevPoint.current = point;
      return;
    }
    const prev = prevPoint.current;
    prevPoint.current = point;
    const justOpened = prev == null;
    if (justOpened) {
      prevFocus.current = document.activeElement as HTMLElement | null;
      requestAnimationFrame(() => {
        containerRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]')?.[0]?.focus();
      });
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        prevFocus.current?.focus();
        onCloseRef.current();
      }
    };
    const onDismiss = () => onCloseRef.current();
    window.addEventListener("keydown", onKey);
    window.addEventListener("click", onDismiss);
    window.addEventListener("scroll", onDismiss, true);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("click", onDismiss);
      window.removeEventListener("scroll", onDismiss, true);
    };
  }, [point]);

  const onMenuKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    const items = Array.from(
      containerRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [],
    );
    if (items.length === 0) return;
    const idx = items.indexOf(document.activeElement as HTMLElement);
    if (e.key === "ArrowDown") {
      e.preventDefault();
      items[(idx + 1) % items.length]?.focus();
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      items[(idx - 1 + items.length) % items.length]?.focus();
    } else if (e.key === "Home") {
      e.preventDefault();
      items[0]?.focus();
    } else if (e.key === "End") {
      e.preventDefault();
      items[items.length - 1]?.focus();
    }
  };

  if (!point || typeof document === "undefined") return null;

  // The menu is taller than the 360px the clamp helper used to assume, so
  // clamp against the rendered height (falls back to 360 pre-mount).
  const menuHeight = containerRef.current?.offsetHeight;
  const pos = clampMenuPoint(point.x, point.y, 220, menuHeight || 360);
  const run = (action: AgentMenuAction) => {
    onAction(action);
    onCloseRef.current();
  };

  return createPortal(
    <div
      ref={containerRef}
      role="menu"
      aria-label={t("agents.context_menu")}
      className="fixed z-50 min-w-52 rounded-lg border border-border bg-popover p-1 text-popover-foreground shadow-md ring-1 ring-foreground/10 max-h-[calc(100vh-16px)] overflow-y-auto"
      style={{ left: pos.x, top: pos.y }}
      onClick={(e) => e.stopPropagation()}
      onContextMenu={(e) => e.preventDefault()}
      onKeyDown={onMenuKeyDown}
    >
      <MenuRow icon={<Terminal />} label={t("agents.interact")} onClick={() => run("interact")} />
      <MenuRow icon={<MoreHorizontal />} label={t("agents.context_open_details")} onClick={() => run("details")} />
      <Separator className="my-1" />
      <MenuRow icon={<Camera />} label={t("agents.screenshot")} onClick={() => run("screenshot")} />
      <MenuRow icon={<Radio />} label={t("agents.beacon_now")} onClick={() => run("beacon_now")} />
      <MenuRow icon={<Clock />} label={t("agents.quick_sleep")} onClick={() => run("sleep")} />
      <MenuRow icon={<StickyNote />} label={t("agents.edit_notes")} onClick={() => run("notes")} />
      <Separator className="my-1" />
      <MenuRow icon={<FolderOpen />} label={t("agents.files_title")} onClick={() => run("files")} />
      <MenuRow icon={<Network />} label={t("agents.dock_cmd_socks")} onClick={() => run("socks")} />
      <MenuRow icon={<Copy />} label={t("agents.copy_id")} onClick={() => run("copy_id")} />
      <MenuRow icon={<RefreshCw />} label={t("agents.rebuild_payload")} onClick={() => run("rebuild")} />
      <MenuRow icon={<IdCard />} label={`${t("nav.beacon_token")} · ${t("generate.quality_hardened")}`} onClick={() => run("tokens")} />
      <MenuRow icon={<Monitor />} label={`${t("nav.beacon_screen")} · ${t("generate.quality_hardened")}`} onClick={() => run("screen")} />
      <Separator className="my-1" />
      <div className="px-2 py-1 text-micro-sm uppercase tracking-wider text-muted-foreground">
        {t("agents.context_danger")}
      </div>
      <MenuRow icon={<Skull />} label={t("agents.kill_agent")} onClick={() => run("kill")} danger />
      <MenuRow icon={<Trash2 />} label={t("agents.uninstall")} onClick={() => run("uninstall")} danger />
      <MenuRow icon={<Trash2 />} label={t("agents.delete")} onClick={() => run("delete")} danger />
    </div>,
    document.body,
  );
}

function MenuRow({
  icon,
  label,
  onClick,
  danger,
}: {
  icon: React.ReactNode;
  label: string;
  onClick: () => void;
  danger?: boolean;
}) {
  return (
    <Button
      type="button"
      role="menuitem"
      variant="ghost"
      size="sm"
      onClick={onClick}
      className={`h-8 w-full justify-start gap-2 px-2 font-normal ${danger ? "text-destructive-ink hover:bg-destructive/10 hover:text-destructive-ink" : ""}`}
    >
      {icon}
      {label}
    </Button>
  );
}
