
import { useEffect, useId, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog";
import { useI18n } from "@/lib/i18n";

export function ConfirmModal({ open, title, message, confirmText, cancelText, danger, requireText, onConfirm, onCancel }: {
  open: boolean;
  title: string;
  message: string;
  confirmText?: string;
  cancelText?: string;
  danger?: boolean;
  requireText?: string;
  onConfirm: () => void | Promise<void>;
  onCancel: () => void;
}) {
  const { t } = useI18n();
  const typedId = useId();
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const busyRef = useRef(false);
  const generationRef = useRef(0);
  const resolvedConfirm = confirmText || t("common.confirm");
  const resolvedCancel = cancelText || t("common.cancel");
  const matched = !requireText || typed.trim() === requireText;

  useEffect(() => {
    generationRef.current += 1;
    busyRef.current = false;
    if (open) {
      setTyped("");
      setBusy(false);
    }
    return () => { generationRef.current += 1; };
  }, [open]);

  const handleConfirm = () => {
    if (!open || !matched || busyRef.current) return;
    const generation = generationRef.current;
    busyRef.current = true;
    setBusy(true);
    const finish = () => {
      // Closing/reopening or unmounting invalidates the previous operation.
      if (generation !== generationRef.current) return;
      busyRef.current = false;
      setBusy(false);
    };
    try {
      void Promise.resolve(onConfirm()).then(finish, finish);
    } catch {
      finish();
    }
  };

  return (
    <Dialog open={open} onOpenChange={(v) => !v && onCancel()}>
      <DialogContent size="md" aria-busy={busy}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        <DialogDescription>{message}</DialogDescription>
        {requireText && (
          <div>
            <label htmlFor={typedId} className="block text-xs text-muted-foreground mb-1.5">
              {t("common.type_to_confirm").replace("{text}", requireText)}
            </label>
            <Input
              id={typedId}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              placeholder={requireText}
              className="font-mono"
            />
          </div>
        )}
        <DialogFooter>
          <Button variant="ghost" onClick={onCancel} disabled={busy}>{resolvedCancel}</Button>
          <Button variant={danger ? "destructive" : "default"} disabled={!matched || busy} onClick={handleConfirm}>{resolvedConfirm}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
