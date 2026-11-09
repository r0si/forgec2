
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog";
import { useI18n } from "@/lib/i18n";
import { Banner } from "@/components/ui/banner";
import { COMMAND_TYPES } from "./types";
import { credActionDef } from "@/lib/cred-quality";
import { EVASION_TECHNIQUES, EVASION_GROUPS } from "@/lib/evasion-techniques";

interface BulkCommandModalProps {
  open: boolean;
  selectedCount: number;
  cmdType: string; setCmdType: (v: string) => void;
  cmdText: string; setCmdText: (v: string) => void;
  onSubmit: () => void;
  onClose: () => void;
}

export function BulkCommandModal({
  open, selectedCount,
  cmdType, setCmdType,
  cmdText, setCmdText,
  onSubmit, onClose,
}: BulkCommandModalProps) {
  const { t } = useI18n();
  const [loading, setLoading] = useState(false);
  const [confirmStep, setConfirmStep] = useState(false);

  const isDestructive = cmdType === "kill" || cmdType === "uninstall";

  const handleSubmit = async () => {
    if (isDestructive && !confirmStep) {
      setConfirmStep(true);
      return;
    }
    setLoading(true);
    try {
      await onSubmit();
    } finally {
      setLoading(false);
      setConfirmStep(false);
    }
  };

  const handleClose = () => {
    setConfirmStep(false);
    setLoading(false);
    onClose();
  };

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent size="md" showCloseButton={false}>
        <DialogHeader>
          <DialogTitle>{t("agents.execute_command")}</DialogTitle>
        </DialogHeader>
        <p className="text-xs text-muted-foreground">
          {t("agents.send_command_to").replace("{n}", String(selectedCount)).replace("{s}", selectedCount !== 1 ? "s" : "")}
        </p>
        {confirmStep && isDestructive && (
          <Banner tone="destructive">
            {cmdType === "kill"
              ? t("agents.confirm_bulk_kill_msg").replace("{count}", String(selectedCount))
              : t("agents.confirm_bulk_uninstall_msg").replace("{count}", String(selectedCount))}
          </Banner>
        )}
        <div className="space-y-3">
          <div>
            <span className="block text-xs font-medium text-muted-foreground mb-1" id="cmd-type-label">{t("agents.command_type")}</span>
            <Select value={cmdType} onValueChange={(v) => { if (v !== null) { setCmdType(v); setConfirmStep(false); } }} disabled={loading} aria-labelledby="cmd-type-label">
              <SelectTrigger className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {COMMAND_TYPES.map((ct) => (
                  <SelectItem key={ct.value} value={ct.value}>
                    {t(ct.labelKey)}
                    {credActionDef(ct.value)?.quality === "scripted" ? ` (${t("generate.quality_scripted")})` : ""}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          {cmdType === "run_evasion" && (
            <div>
              <span className="block text-xs font-medium text-muted-foreground mb-1">{t("agents.evasion_select")}</span>
              <Select value={cmdText} onValueChange={(v) => { if (v !== null) setCmdText(v); }} disabled={loading}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder={t("agents.evasion_select")} />
                </SelectTrigger>
                <SelectContent>
                  {EVASION_GROUPS.map((group) => (
                    <div key={group}>
                      <div className="px-2 py-1 text-xs font-semibold text-muted-foreground">{group}</div>
                      {EVASION_TECHNIQUES.filter((tech) => tech.group === group).map((tech) => (
                        <SelectItem key={tech.value} value={tech.value}>{t(tech.labelKey)}</SelectItem>
                      ))}
                    </div>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}
          {cmdType !== "screenshot" && cmdType !== "kill" && cmdType !== "uninstall" && cmdType !== "run_evasion" && (
            <div>
              <span className="block text-xs font-medium text-muted-foreground mb-1" id="cmd-text-label">
                {cmdType === "sleep" ? t("agents.interval_seconds") : t("agents.command")}
              </span>
              <Input
                id="cmd-text-input"
                type="text"
                value={cmdText}
                onChange={(e) => setCmdText(e.target.value)}
                placeholder={cmdType === "shell" ? "whoami" : cmdType === "ps" ? "" : cmdType === "ls" ? "C:\\" : "30"}
                className="h-11"
                autoFocus
                disabled={loading}
                aria-labelledby="cmd-text-label"
                onKeyDown={(e) => { if (e.key === "Enter") handleSubmit(); }}
              />
            </div>
          )}
          {cmdType === "screenshot" && (
            <p className="text-xs text-muted-foreground">{t("agents.no_params_needed")}</p>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={handleClose} disabled={loading}>{t("common.cancel")}</Button>
          <Button
            variant={isDestructive ? "destructive" : "default"}
            onClick={handleSubmit}
            disabled={selectedCount === 0 || loading}
          >
            {loading ? t("common.loading") : confirmStep ? t("agents.execute_confirm") : t("agents.execute_btn")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
