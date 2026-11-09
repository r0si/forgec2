
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { NAV_SECTIONS, filterNavByPermissions } from "@/lib/navigation";
import { prefetchRoute } from "@/lib/route-prefetch";
import { useI18n } from "@/lib/i18n";
import { useAppStore } from "@/lib/store";
import { useAgentList } from "@/lib/hooks/useAgentList";
import { isEditableTarget } from "@/lib/interact-workspace";
import { Search, Server, Terminal } from "lucide-react";
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
import { ScrollArea } from "@/components/ui/scroll-area";

interface PaletteItem {
  href: string;
  label: string;
  section: string;
  icon: React.ComponentType<{ className?: string }>;
  subtitle?: string;
}

function normalize(s: string): string {
  return s.toLowerCase().replace(/[-_/]/g, " ");
}

/** Every whitespace token must appear in the haystack (substring match). */
function matchesQuery(q: string, ...parts: string[]): boolean {
  const tokens = q.split(/\s+/).filter(Boolean);
  if (tokens.length === 0) return true;
  const hay = normalize(parts.filter(Boolean).join(" "));
  return tokens.every((tok) => hay.includes(tok));
}

export default function CommandPalette() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const open = useAppStore((s) => s.commandPaletteOpen);
  const setOpen = useAppStore((s) => s.setCommandPaletteOpen);
  const permissions = useAppStore((s) => s.currentPermissions);
  // The palette is mounted globally, but most sessions never open it. Avoid
  // fetching the full fleet until it is actually used; the shared cache still
  // makes subsequent opens instant and lets other pages reuse the snapshot.
  const { agents, error: agentListError } = useAgentList({ enabled: open });
  const [query, setQuery] = useState("");
  const [activeIndex, setActiveIndex] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  const items: PaletteItem[] = useMemo(() => {
    const out: PaletteItem[] = [];
    for (const section of NAV_SECTIONS) {
      for (const item of filterNavByPermissions(section.items, permissions)) {
        if (item.discoverable === false) continue;
        out.push({
          href: item.href,
          label: t(item.labelKey),
          section: t("section." + section.titleKey),
          icon: item.icon,
        });
      }
    }
    return out;
  }, [t, permissions]);

  const agentItems: PaletteItem[] = useMemo(() => {
    const q = normalize(query.trim());
    if (!q) return [];
    const out: PaletteItem[] = [];
    const shellLabel = t("nav.beacon_shell_title");
    for (const agent of agents) {
      const id = agent.id;
      if (!id) continue;
      const name = agent.hostname || id;
      const meta = [agent.username, agent.ip].filter(Boolean).join(" \u00b7 ");
      const base = [agent.hostname, agent.ip, agent.username, agent.os].filter(Boolean).join(" ");
      if (matchesQuery(q, base, name, meta, id)) {
        out.push({
          href: `/beacons/${id}`,
          label: name,
          section: t("palette.agents"),
          icon: Server,
          subtitle: meta,
        });
      }
      const shellHay = `${base} shell ${shellLabel} ${name} ${shellLabel}`;
      if (matchesQuery(q, shellHay, `${name} ${shellLabel}`, meta, `/beacons/${id}/shell`)) {
        out.push({
          href: `/beacons/${id}/shell`,
          label: `${name} → ${shellLabel}`,
          section: t("palette.agents"),
          icon: Terminal,
          subtitle: meta || shellLabel,
        });
      }
      if (out.length >= 8) break;
    }
    return out;
  }, [agents, query, t]);

  const filtered = useMemo(() => {
    const q = normalize(query.trim());
    if (!q) return items;
    return [...items, ...agentItems].filter((i) =>
      matchesQuery(q, i.label, i.href, i.section, i.subtitle ?? ""),
    );
  }, [items, agentItems, query]);

  useEffect(() => {
    if (open) {
      setQuery("");
      setActiveIndex(0);
      requestAnimationFrame(() => inputRef.current?.focus());
    }
  }, [open]);

  const close = useCallback(() => setOpen(false), [setOpen]);

  const handleSelect = useCallback((index: number) => {
    const item = filtered[index];
    if (!item) return;
    close();
    navigate(item.href);
  }, [filtered, close, navigate]);

  // G10 fix: skip e.repeat (held key auto-repeat) and skip editable targets
  // so Ctrl+K does not hijack xterm's native kill-line shortcut.
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.repeat) return;
      if (isEditableTarget(e.target)) return;
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setOpen((v: boolean) => !v);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [setOpen]);

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (e: KeyboardEvent) => {
      const total = filtered.length;
      if (e.key === "ArrowDown") {
        e.preventDefault();
        setActiveIndex((i) => (i + 1) % total);
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        setActiveIndex((i) => (i - 1 + total) % total);
      } else if (e.key === "Enter") {
        e.preventDefault();
        handleSelect(activeIndex);
      } else if (e.key === "Escape") {
        e.preventDefault();
        close();
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [open, filtered, activeIndex, handleSelect, close]);

  useEffect(() => {
    setActiveIndex(0);
  }, [query]);

  useEffect(() => {
    const el = listRef.current?.querySelector('[data-active="true"]');
    if (el && typeof el.scrollIntoView === "function") {
      el.scrollIntoView({ block: "nearest" });
    }
  }, [activeIndex]);

  const { groups, indexMap } = useMemo(() => {
    const m = new Map<string, PaletteItem[]>();
    const idx = new Map<PaletteItem, number>();
    filtered.forEach((item, i) => {
      idx.set(item, i);
      const list = m.get(item.section) || [];
      list.push(item);
      m.set(item.section, list);
    });
    return { groups: m, indexMap: idx };
  }, [filtered]);

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) close(); }}>
      <DialogContent size="xl"
        showCloseButton={false}
        className="top-[18vh] translate-y-0 gap-0 overflow-hidden p-0"
        aria-label={t("palette.title")}
      >
        <DialogTitle className="sr-only">{t("palette.title")}</DialogTitle>
        <div className="flex items-center gap-2 border-b border-border/60 px-4 py-3">
          <Search className="size-4 shrink-0 text-muted-foreground" />
          <Input
            ref={inputRef}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("palette.placeholder")}
            aria-label={t("palette.placeholder")}
            role="combobox"
            aria-expanded={open}
            aria-controls="command-palette-list"
            aria-autocomplete="list"
            aria-activedescendant={activeIndex >= 0 ? `palette-option-${activeIndex}` : undefined}
            className="h-8 flex-1 border-0 bg-transparent px-0 shadow-none focus-visible:ring-0 dark:bg-transparent"
          />
          <Kbd>Esc</Kbd>
        </div>

        <ScrollArea className="max-h-[45vh]">
          <div ref={listRef} id="command-palette-list" role="listbox" aria-label={t("palette.title")} className="py-2">
            {/* Agent entries are silently absent while the list is unread. */}
            {agentListError && normalize(query.trim()) !== "" && (
              <div role="alert" className="mx-4 mb-2 rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning-ink">
                {t("palette.agents_unreadable")}
              </div>
            )}
            {filtered.length === 0 ? (
              <div className="px-4 py-10 text-center text-sm text-muted-foreground">{t("palette.no_results")}</div>
            ) : (
              [...groups.entries()].map(([section, group]) => (
                <div key={section}>
                  <div className="px-4 pt-3 pb-1.5 text-micro-sm uppercase tracking-wider text-muted-foreground">
                    {section}
                  </div>
                  {group.map((item) => {
                    const globalIndex = indexMap.get(item) ?? 0;
                    const Icon = item.icon;
                    return (
                      <Button
                        key={item.href}
                        type="button"
                        variant="ghost"
                        role="option"
                        id={`palette-option-${globalIndex}`}
                        aria-selected={activeIndex === globalIndex}
                        data-active={activeIndex === globalIndex}
                        onClick={() => handleSelect(globalIndex)}
                        onMouseEnter={() => prefetchRoute(item.href)}
                        className={`h-auto w-full justify-start gap-3 rounded-none px-4 py-2.5 text-left text-sm ${activeIndex === globalIndex ? "bg-primary/10" : ""}`}
                      >
                        <span className="flex size-6 shrink-0 items-center justify-center rounded-md bg-secondary/70">
                          <Icon className="size-3.5 text-muted-foreground" />
                        </span>
                        <span className="flex min-w-0 flex-col">
                          <span className="truncate text-foreground">{item.label}</span>
                          {item.subtitle && (
                            <span className="truncate text-xs text-muted-foreground">{item.subtitle}</span>
                          )}
                        </span>
                        <span className="ml-auto max-w-40 truncate font-mono text-xs text-muted-foreground">{item.href}</span>
                      </Button>
                    );
                  })}
                </div>
              ))
            )}
          </div>
        </ScrollArea>
      </DialogContent>
    </Dialog>
  );
}
