import { useCallback, useEffect, useRef, useState } from "react";
import { ConfirmModal } from "@/components/ui/confirm-modal";
import { useI18n } from "@/lib/i18n";

interface ConfirmOptions {
  title?: string;
  message: string;
  confirmText?: string;
  cancelText?: string;
  danger?: boolean;
  requireText?: string;
}

interface ConfirmState {
  id: number;
  opts: ConfirmOptions;
  resolve: (value: boolean) => void;
}

/**
 * Promise-based confirmation dialog. Returns a boolean promise that resolves
 * with the user's choice, and renders the modal via `confirm.modal`.
 *
 *   const confirm = useConfirm();
 *   const ok = await confirm({ message: "Delete?", danger: true });
 *   if (ok) { ... }
 *   ...
 *   {confirm.modal}
 */
export function useConfirm() {
  const { t } = useI18n();
  const [state, setState] = useState<ConfirmState | null>(null);
  const stateRef = useRef<ConfirmState | null>(null);
  const sequenceRef = useRef(0);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      stateRef.current?.resolve(false);
      stateRef.current = null;
    };
  }, []);

  // G3 fix: when a second confirm() is called while one is already open,
  // resolve the pending promise as false before overwriting, preventing hangs.
  const confirm = useCallback((opts: ConfirmOptions) => {
    if (!mountedRef.current) return Promise.resolve(false);
    return new Promise<boolean>((resolve) => {
      if (stateRef.current) stateRef.current.resolve(false);
      stateRef.current = { id: ++sequenceRef.current, opts: { ...opts }, resolve };
      setState(stateRef.current);
    });
  }, []);

  const close = useCallback((request: ConfirmState, value: boolean) => {
    // Events from a replaced prompt must never settle the current request.
    if (stateRef.current !== request) return;
    request.resolve(value);
    stateRef.current = null;
    setState(null);
  }, []);

  const modal = state ? (
    <ConfirmModal
      key={state.id}
      open
      title={state.opts.title || t("common.confirm")}
      message={state.opts.message}
      confirmText={state.opts.confirmText}
      cancelText={state.opts.cancelText}
      danger={state.opts.danger}
      requireText={state.opts.requireText}
      onConfirm={() => close(state, true)}
      onCancel={() => close(state, false)}
    />
  ) : null;

  return { confirm, modal };
}
