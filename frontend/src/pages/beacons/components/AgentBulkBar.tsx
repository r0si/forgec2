
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Banner } from "@/components/ui/banner";
import { useI18n } from "@/lib/i18n";
import { enumLabel } from "@/lib/utils";
import type { BulkResult } from "./types";
import { Camera, Clock, History, Power, Radio, Terminal, Trash2, X } from "lucide-react";

interface AgentBulkBarProps {
  selected: Set<string>;
  bulkMode: boolean;
  bulkResults: BulkResult[];
  showResults: boolean; setShowResults: (v: boolean) => void;
  onBulkShell: () => void;
  onBulkScreenshot: () => void;
  onBulkBeaconNow: () => void;
  onBulkSleep: () => void;
  onBulkKill: () => void;
  onBulkUninstall: () => void;
  onBulkDelete: () => void;
  onClearSelection: () => void;
  actionMsg: string | null; dismissActionMsg: () => void;
}

export function AgentBulkBar({
  selected, bulkMode,
  bulkResults, showResults, setShowResults,
  onBulkShell, onBulkScreenshot, onBulkBeaconNow, onBulkSleep,
  onBulkKill, onBulkUninstall, onBulkDelete,
  onClearSelection,
  actionMsg, dismissActionMsg,
}: AgentBulkBarProps) {
  const { t } = useI18n();
  return (
    <>
      {actionMsg && (
        <Banner tone="info" className="mb-3" action={<Button variant="ghost" size="icon-xs" onClick={dismissActionMsg} aria-label={t("common.close")}><X className="size-4" /></Button>}>
          {actionMsg}
        </Banner>
      )}

      {bulkMode && selected.size > 0 && (
        <div className="mb-3 px-4 py-3 bg-sidebar border border-border rounded-lg flex flex-wrap items-center gap-2 shadow-sm">
          <span className="text-sm text-foreground font-medium mr-2">{t("agents.n_selected").replace("{n}", String(selected.size))}</span>
          <Button size="sm" onClick={onBulkShell} ><Terminal className="size-4" />{t("agents.execute_command")}</Button>
          <Button size="sm" onClick={onBulkKill} className="bg-destructive-solid hover:bg-destructive-solid/90 text-destructive-foreground"><Power className="size-4" />{t("agents.kill")}</Button>
          <Button size="sm" onClick={onBulkUninstall} className="bg-destructive-solid hover:bg-destructive-solid/90 text-destructive-foreground"><Trash2 className="size-4" />{t("agents.uninstall")}</Button>
          <Button variant="ghost" size="sm" onClick={onClearSelection} className="ml-auto text-muted-foreground hover:text-foreground">{t("agents.clear_selection")}</Button>
        </div>
      )}

      {!bulkMode && selected.size > 0 && (
        <div className="mb-3 px-4 py-3 bg-sidebar border border-border rounded-lg flex flex-wrap items-center gap-2 shadow-sm">
          <span className="text-sm text-foreground font-medium mr-2">{t("agents.n_selected").replace("{n}", String(selected.size))}</span>
          <Button size="sm" onClick={onBulkShell} ><Terminal className="size-4" />{t("nav.beacon_shell")}</Button>
          <Button size="sm" variant="secondary" onClick={onBulkScreenshot}><Camera className="size-4" />{t("agents.screenshot")}</Button>
          <Button size="sm" variant="secondary" onClick={onBulkBeaconNow}><Radio className="size-4" />{t("agents.beacon_now")}</Button>
          <Button size="sm" variant="secondary" onClick={onBulkSleep}><Clock className="size-4" />{t("agents.sleep")}</Button>
          <Button size="sm" onClick={onBulkDelete} className="bg-destructive-solid hover:bg-destructive-solid/90 text-destructive-foreground"><Trash2 className="size-4" />{t("agents.delete")}</Button>
          <Button variant="ghost" size="sm" onClick={onClearSelection} className="ml-auto text-muted-foreground hover:text-foreground">{t("agents.clear")}</Button>
        </div>
      )}

      {showResults && (
        <Card className="p-(--card-spacing) mb-4 gap-0">
          <div className="flex items-center justify-between mb-3">
            <h3 className="text-sm font-semibold text-foreground"><History className="size-4" />{t("agents.recent_bulk_ops")}</h3>
            <Button variant="ghost" size="icon-xs" onClick={() => setShowResults(false)} aria-label={t("common.close")}><X className="size-4" /></Button>
          </div>
          {bulkResults.length === 0 ? (
            <p className="text-xs text-muted-foreground text-center py-4">{t("agents.no_bulk_ops")}</p>
          ) : (
            <div className="space-y-2 max-h-64 overflow-y-auto">
              {bulkResults.map((r, i) => (
                <div key={r.id || i} className="flex items-center justify-between px-3 py-2 bg-muted rounded-lg text-xs">
                  <div className="flex items-center gap-2 min-w-0">
                    <span className={`size-6 rounded-lg flex items-center justify-center text-primary-foreground text-micro-sm font-bold shrink-0 ${
                      r.task_type === "kill" ? "bg-chart-4" :
                      r.task_type === "uninstall" ? "bg-destructive" :
                      "bg-primary/100"
                    }`}>
                      {r.task_type === "kill" ? <Power className="size-3" /> :
                        r.task_type === "uninstall" ? <Trash2 className="size-3" /> :
                        r.task_type === "screenshot" ? <Camera className="size-3" /> :
                        <Terminal className="size-3" />}
                    </span>
                    <div className="min-w-0">
                      <span className="font-medium text-foreground">{enumLabel(t, "command.type", r.task_type)}</span>
                      {r.command && <span className="ml-1.5 text-muted-foreground">{r.command}</span>}
                      {r.operator && <span className="ml-2 text-muted-foreground">{r.operator}</span>}
                    </div>
                  </div>
                  <span className="text-muted-foreground shrink-0">
                    {t("agents.n_tasks").replace("{n}", String(r.tasks_created ?? 0))}
                    {r.failed > 0 && <span className="ml-1 text-destructive-ink">({t("agents.n_failed").replace("{n}", String(r.failed))})</span>}
                  </span>
                </div>
              ))}
            </div>
          )}
        </Card>
      )}
    </>
  );
}
