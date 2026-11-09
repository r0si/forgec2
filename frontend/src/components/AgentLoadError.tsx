import { AlertTriangle } from "lucide-react";
import { useI18n } from "@/lib/i18n";

/**
 * Inline warning for pages whose agent list failed to load. Without it an
 * unread list looks identical to a deployment with no agents, so target
 * selectors silently offer nothing.
 */
export function AgentLoadError({ message, className = "" }: { message: string | null; className?: string }) {
  const { t } = useI18n();
  if (!message) return null;
  return (
    <div
      role="alert"
      className={`flex flex-wrap items-center gap-2 rounded-xl border border-warning/30 bg-warning/10 px-4 py-2.5 text-xs text-warning-ink ${className}`}
    >
      <AlertTriangle className="size-4 shrink-0" aria-hidden="true" />
      <span className="min-w-0 flex-1">
        {t("error.agents_list_unreadable", { message })}
      </span>
    </div>
  );
}
