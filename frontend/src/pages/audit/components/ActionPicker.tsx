import { useMemo, useState } from "react";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scroll-area";
import { useI18n } from "@/lib/i18n";
import { auditActionLabel } from "./audit-log";
import { Search } from "lucide-react";

export interface ActionCatalogueEntry {
  action: string;
  category: string;
  count: number;
}

interface ActionPickerProps {
  /** The catalogue the server published (every action it has rows for). */
  actions: ActionCatalogueEntry[];
  /** The selected action code, or "" for "all actions". */
  value: string;
  onChange: (action: string) => void;
  /** Restrict the list to one category (the category filter at work). */
  category?: string;
  allLabel: string;
  placeholder: string;
  /** Shown when the search matched nothing. */
  emptyLabel: string;
}

/** How many rows the list renders at once. The catalogue holds hundreds of
 *  actions on a busy deployment; rendering them all would make the popover
 *  slow for no gain - the search is the way through them. */
const MAX_ROWS = 100;

/**
 * A searchable picker for one of the hundreds of actions the audit log knows.
 *
 * The plain select could not carry them: a dropdown with a few hundred
 * entries is a list nobody scrolls, and the operator who wants "the one
 * action about listeners" has to know its exact code. This lists the actions
 * the server actually has rows for, with their counts, and filters them by
 * the readable label, the raw code or the category.
 */
export function ActionPicker({
  actions,
  value,
  onChange,
  category = "",
  allLabel,
  placeholder,
  emptyLabel,
}: ActionPickerProps) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");

  const pool = useMemo(
    () => (category ? actions.filter((a) => a.category === category) : actions),
    [actions, category],
  );

  const matches = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return pool;
    return pool.filter((a) => {
      const label = auditActionLabel(t, a.action).toLowerCase();
      return (
        a.action.toLowerCase().includes(needle) ||
        label.includes(needle) ||
        a.category.toLowerCase().includes(needle)
      );
    });
  }, [pool, query, t]);

  const selected = actions.find((a) => a.action === value);

  return (
    <Popover
      open={open}
      onOpenChange={(next: boolean) => {
        setOpen(next);
        if (!next) setQuery("");
      }}
    >
      <PopoverTrigger
        render={
          <Button variant="outline" className="w-[210px] justify-start" aria-label={allLabel} />
        }
      >
        <span className="truncate">
          {value ? auditActionLabel(t, value) : allLabel}
        </span>
      </PopoverTrigger>
      <PopoverContent align="start" sideOffset={4} className="w-[320px] p-2">
        <div className="relative mb-2">
          <Search className="pointer-events-none absolute left-2 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={placeholder}
            aria-label={placeholder}
            className="h-8 pl-8 text-sm"
          />
        </div>
        <ScrollArea className="max-h-[280px]">
          <div className="flex flex-col gap-0.5">
            <Button
              variant="ghost"
              size="xs"
              className="w-full justify-start text-xs"
              aria-pressed={value === ""}
              onClick={() => {
                onChange("");
                setOpen(false);
              }}
            >
              {allLabel}
            </Button>
            {matches.length === 0 ? (
              <span className="px-2 py-3 text-xs text-muted-foreground">{emptyLabel}</span>
            ) : (
              matches.slice(0, MAX_ROWS).map((entry) => (
                <Button
                  key={entry.action}
                  variant="ghost"
                  size="xs"
                  className="w-full justify-start text-xs"
                  aria-pressed={value === entry.action}
                  onClick={() => {
                    onChange(entry.action);
                    setOpen(false);
                  }}
                >
                  <span className="truncate">{auditActionLabel(t, entry.action)}</span>
                  <span className="ml-auto shrink-0 font-mono text-[10px] text-muted-foreground">
                    {entry.count}
                  </span>
                </Button>
              ))
            )}
            {matches.length > MAX_ROWS && (
              <span className="px-2 py-2 text-[10px] text-muted-foreground">
                {`${matches.length} · ${MAX_ROWS}`}
              </span>
            )}
          </div>
        </ScrollArea>
        {selected && (
          <span className="mt-2 block truncate font-mono text-[10px] text-muted-foreground">
            {selected.action}
          </span>
        )}
      </PopoverContent>
    </Popover>
  );
}
