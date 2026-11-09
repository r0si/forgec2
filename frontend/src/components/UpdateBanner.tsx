
import { useEffect, useState } from "react";
import { useWS } from "@/lib/wsContext";
import { useI18n } from "@/lib/i18n";
import { usePermissions } from "@/lib/hooks/usePermissions";
import { Button } from "@/components/ui/button";
import { toastSoon } from "@/lib/toast";
import { ArrowUpCircle, X } from "lucide-react";
import { safeHref } from "@/lib/safeUrl";
import { bannerSurface } from "@/components/ui/banner";
import { cn } from "@/lib/utils";
import { openUpdateDialog } from "@/components/UpdateDialog";

const DISMISS_KEY = "forgec2_update_dismissed";

export default function UpdateBanner({ currentVersion }: { currentVersion?: string }) {
  const { subscribe } = useWS();
  const { t } = useI18n();
  const { role } = usePermissions();
  const [info, setInfo] = useState<{ latest: string; downloadUrl?: string } | null>(null);
  const [dismissed, setDismissed] = useState(false);
  // The hot update endpoint is admin-only. Offering the button to every
  // operator only produced a 403, so the banner keeps the release link and
  // leaves the installation to an administrator.
  const isAdmin = (role || "").toLowerCase() === "admin";

  useEffect(() => {
    try {
      const d = localStorage.getItem(DISMISS_KEY);
      if (d) Promise.resolve().then(() => setDismissed(true));
    } catch {
      toastSoon((toast) => toast.error(t("update_banner.toast.storage_failed")));
    }
  }, [t]);

  useEffect(() => {
    return subscribe((msg) => {
      if (msg.type === "update_available") {
        const latest = String(msg.latest || "");
        let dismissedVer: string | null = null;
        try {
          dismissedVer = localStorage.getItem(DISMISS_KEY);
        } catch {
          toastSoon((toast) => toast.error(t("update_banner.toast.storage_failed")));
        }
        if (!latest || dismissedVer === latest) return;
        setInfo({
          latest,
          downloadUrl: safeHref(msg.download_url),
        });
        setDismissed(false);
        // Push window: open the update dialog for the new version. Only an
        // administrator can install it, so the dialog stays closed for the
        // others.
        if (isAdmin) {
          openUpdateDialog({ latest, downloadUrl: safeHref(msg.download_url) ?? undefined });
        }
      }
    });
  }, [subscribe, t, isAdmin]);

  if (!info || dismissed) return null;

  const dismiss = () => {
    setDismissed(true);
    try {
      localStorage.setItem(DISMISS_KEY, info.latest);
    } catch {
      toastSoon((toast) => toast.error(t("update_banner.toast.storage_failed")));
    }
  };

  const downloadHref = info.downloadUrl ?? "https://github.com/forgec2/forgec2/releases";

  return (
    <div className={cn(bannerSurface("info"), "flex w-full items-center justify-between gap-3 px-4 py-2.5 animate-fade-in sm:px-5")} aria-live="polite">
      <div className="flex items-center gap-2 min-w-0">
        <ArrowUpCircle className="size-4 text-info-ink shrink-0" />
        <span className="mono-cell text-compact text-foreground/90 truncate">
          {t("update_banner.available")}: <strong className="font-mono">{info.latest}</strong>
          {currentVersion && <span className="text-muted-foreground"> {t("update_banner.current", { current: currentVersion })}</span>}
        </span>
      </div>
      <div className="flex items-center gap-3 shrink-0 ml-3">
        {isAdmin ? (
          <Button
            variant="ghost"
            size="sm"
            className="text-info-ink underline-offset-2 hover:underline hidden sm:inline-flex"
            onClick={() => openUpdateDialog({ latest: info.latest, downloadUrl: info.downloadUrl ?? undefined })}
          >
            {t("update_banner.update_now")}
          </Button>
        ) : (
          <span className="hidden sm:inline text-xs text-muted-foreground">
            {t("update_banner.admin_only")}
          </span>
        )}
        <a
          href={downloadHref}
          target="_blank"
          rel="noopener noreferrer"
          className="text-sm text-info-ink font-medium underline-offset-2 hover:underline transition-colors hidden sm:inline"
        >
          {t("update_banner.download")}
        </a>
        <Button variant="ghost" size="icon-sm" onClick={dismiss} className="text-muted-foreground hover:text-foreground" aria-label={t("common.dismiss")}>
          <X className="size-4" />
        </Button>
      </div>
    </div>
  );
}
