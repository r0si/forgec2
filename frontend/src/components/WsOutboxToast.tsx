import { useEffect } from "react";
import { toastSoon } from "@/lib/toast";
import { useI18n } from "@/lib/i18n";
import { WS_OUTBOX_DROP_EVENT } from "@/lib/wsContext";

/**
 * Surfaces offline WS outbox overflows the provider reports via
 * WS_OUTBOX_DROP_EVENT. The stable toast id collapses repeated sheds into
 * one updating toast instead of stacking one per dropped frame.
 */
export default function WsOutboxToast() {
  const { t } = useI18n();

  useEffect(() => {
    const onDrop = (e: Event) => {
      const dropped = (e as CustomEvent<{ dropped?: number }>).detail?.dropped ?? 1;
      toastSoon((toast) => toast.warning(t("network.outbox_dropped", { n: dropped }), { id: "ws-outbox-drop" }));
    };
    window.addEventListener(WS_OUTBOX_DROP_EVENT, onDrop);
    return () => window.removeEventListener(WS_OUTBOX_DROP_EVENT, onDrop);
  }, [t]);

  return null;
}
