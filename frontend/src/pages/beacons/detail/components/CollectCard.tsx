import { memo, type ReactNode } from "react";
import { SectionCard } from "@/components/ui/section-card";
import { EmptyState } from "@/components/ui/empty-state";
import { CopyButton } from "@/components/ui/copy-button";
import type { Hue } from "@/lib/ui/statusStyles";
import type { LucideIcon } from "lucide-react";

interface CollectCardProps {
  title: string;
  icon: LucideIcon;
  /** Accent hue for the header icon. */
  tone?: Hue;
  /** Buttons / badges rendered at the right of the header. */
  headerRight?: ReactNode;
  /** Extra controls rendered above the result area (inputs, textareas…). */
  children?: ReactNode;
  /** Empty-state content shown while `result` is null. */
  emptyIcon: React.ComponentType<{ className?: string }>;
  emptyTitle: string;
  emptyHint: string;
  /** Collected output; null = not collected yet. */
  result: string | null;
  /** Label shown above the result (defaults to `title`). */
  resultLabel?: string;
  /** Fully custom replacement for the empty/result block
   * (e.g. Clipboard's timestamped history list). */
  resultOverride?: ReactNode;
  /** Extra content below the result (hints, history lists…). */
  footer?: ReactNode;
  /** Max height class for the result block. */
  resultMaxHeight?: string;
}

/**
 * Shared shell for the agent detail "collect" sections: header with
 * actions, optional controls, then either an empty state or a
 * copyable `<pre>` result block. Keeps the visual language identical
 * across Recon / Keylogger / Registry / … sections.
 */
export default memo(function CollectCard({
  title,
  icon,
  tone,
  headerRight,
  children,
  emptyIcon,
  emptyTitle,
  emptyHint,
  result,
  resultLabel,
  resultOverride,
  footer,
  resultMaxHeight = "max-h-72",
}: CollectCardProps) {
  return (
    <SectionCard title={title} icon={icon} tone={tone} action={headerRight} className="mb-(--page-gap)">
      <div className="space-y-3 p-3">
        {children}
        {resultOverride ?? (result === null ? (
          <EmptyState icon={emptyIcon} title={emptyTitle} message={emptyHint} />
        ) : (
          <div>
            <div className="mb-1 flex items-center justify-between gap-2">
              <span className="truncate font-mono text-xs text-muted-foreground">{resultLabel ?? title}</span>
              <CopyButton text={result} label={title} size="xs" />
            </div>
            <pre
              className={`overflow-auto whitespace-pre-wrap rounded-lg border border-border bg-muted/50 p-3 font-mono text-xs ${resultMaxHeight}`}
            >
              {result}
            </pre>
          </div>
        ))}
        {footer}
      </div>
    </SectionCard>
  );
});
