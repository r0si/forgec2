import type { ComponentType, ReactNode } from "react";
import { cn } from "@/lib/utils";

interface WorkspaceToolbarProps {
  /** Page title. Rendered as the page's single h1. */
  title: ReactNode;
  /** Optional lead icon rendered before the title. */
  icon?: ComponentType<{ className?: string }>;
  /** Small muted line under the title (agent ip, os, resolution…). */
  meta?: ReactNode;
  /** Status pills / agent-id badges rendered next to the title. */
  badges?: ReactNode;
  /** Primary actions, right-aligned; they wrap under the title on narrow screens. */
  actions?: ReactNode;
  /** Secondary content rendered under the title row (tabs, filters…). */
  children?: ReactNode;
  /** `terminal` keeps the shell console's dark, full-bleed chrome. */
  tone?: "default" | "terminal";
  /** Content before the title (back link, breadcrumb chevron…). */
  leading?: ReactNode;
  className?: string;
}

/**
 * Workspace chrome (U9.5): one toolbar band for the agent workspaces (shell,
 * files, screen, remote desktop). It owns the single h1, the title/meta/badge
 * cluster and the action row so the four pages cannot drift apart again.
 *
 * The toolbar is a flex child, not a sticky overlay: workspace pages scroll
 * their content region, so the band only needs to stay out of the scroll box.
 */
export function WorkspaceToolbar({
  title,
  icon: Icon,
  meta,
  badges,
  actions,
  children,
  tone = "default",
  leading,
  className,
}: WorkspaceToolbarProps) {
  const terminal = tone === "terminal";

  return (
    <header
      data-slot="workspace-toolbar"
      data-tone={tone}
      className={cn(
        "flex shrink-0 flex-col border-b",
        terminal
          ? "border-white/10 bg-(--shell-terminal-bg) text-slate-100"
          : "border-border/70 bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/85",
        className,
      )}
    >
      <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2 px-(--page-pad-x) py-2.5">
        {leading ? <div className="flex shrink-0 items-center gap-1">{leading}</div> : null}

        <div className="flex min-w-0 items-center gap-2.5">
          {Icon ? (
            <Icon
              className={cn("size-4 shrink-0", terminal ? "text-slate-400" : "text-muted-foreground")}
              aria-hidden="true"
            />
          ) : null}
          <div className="min-w-0">
            <h1
              className={cn(
                "truncate text-lg font-semibold tracking-tight",
                terminal ? "text-slate-100" : "text-foreground",
              )}
            >
              {title}
            </h1>
            {meta ? (
              <div
                className={cn(
                  "truncate font-mono text-micro-sm",
                  terminal ? "text-slate-400" : "text-muted-foreground",
                )}
              >
                {meta}
              </div>
            ) : null}
          </div>
        </div>

        {badges ? <div className="flex min-w-0 flex-wrap items-center gap-2">{badges}</div> : null}

        {actions ? (
          <div className="ml-auto flex flex-wrap items-center justify-end gap-2">{actions}</div>
        ) : null}
      </div>

      {children ? <div className="w-full min-w-0">{children}</div> : null}
    </header>
  );
}
