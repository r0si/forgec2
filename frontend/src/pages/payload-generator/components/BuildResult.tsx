
import type { ReactNode } from "react";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Spinner } from "@/components/ui/spinner";
import { useI18n } from "@/lib/i18n";
import type { BuildSignature } from "@/types/generate";
import { CheckCircle2, Loader2, ShieldAlert, ShieldCheck, ShieldOff, ShieldX, XCircle } from "lucide-react";

/**
 * Every panel stores "" in state.result on success (the one-liner panel uses
 * the literal sentinel "success"), so any other non-empty string is a build
 * error. The old prefix check ("Error"/"ERROR") missed common failures like
 * "HTTP 500" or compiler output and mislabeled them as success.
 */
function isErrorString(result: ReactNode): boolean {
  if (typeof result !== "string") return false;
  const s = result.trim();
  return s !== "" && s !== "success" && s !== "OK";
}

/**
 * BuildSignatureBadge — Authenticode classification for a completed direct
 * Windows EXE/DLL. Self-signed certificates are intentionally shown as
 * untrusted rather than "valid": Windows will still display Unknown Publisher.
 */
export function BuildSignatureBadge({ signature }: { signature?: BuildSignature }) {
  const { t } = useI18n();
  if (!signature) return null;
  const selfSigned = !!signature.signer && signature.signer === signature.issuer;
  let variant: "success" | "warning" | "destructive" | "outline" = "outline";
  let Icon = ShieldOff;
  let label: string;
  switch (signature.status) {
    case "valid":
      variant = "success";
      Icon = ShieldCheck;
      label = t("generate.signature.valid");
      break;
    case "untrusted_root":
      variant = "warning";
      Icon = ShieldAlert;
      label = selfSigned ? t("generate.signature.self_signed") : t("generate.signature.untrusted_root");
      break;
    case "invalid":
      variant = "destructive";
      Icon = ShieldX;
      label = t("generate.signature.invalid");
      break;
    default:
      label = t("generate.signature.unsigned");
      break;
  }
  const signerLabel = signature.signer
    ? t("generate.signature.signer", { name: signature.signer })
    : undefined;
  const detail = [signerLabel, signature.error].filter(Boolean).join("\n") || undefined;
  return (
    <div className="mt-3 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
      <Badge variant={variant} className="gap-1 rounded-full px-2.5 py-1 text-xs shadow-sm" title={detail}>
        <Icon className="size-3.5" /> {label}
      </Badge>
      {signerLabel && (
        <span className="min-w-0 max-w-full truncate text-xs text-muted-foreground" title={detail}>
          {signerLabel}
        </span>
      )}
    </div>
  );
}

/**
 * BuildResult — unified in-card build feedback:
 * busy → compiling strip with progress · result → success/error alert, plus
 * the Authenticode classification for Windows EXE/DLL builds.
 */
export function BuildResult({ busy, result, signature, hint }: { busy?: boolean; result?: ReactNode; signature?: BuildSignature; hint?: { code: string; text: string } }) {
  const { t } = useI18n();
  if (busy) {
    return (
      <div className="mt-3 flex items-center gap-3 rounded-xl border border-primary/20 bg-gradient-to-r from-primary/10 via-primary/5 to-transparent px-3.5 py-3 shadow-sm">
        <Spinner size="sm" />
        <div className="flex-1 space-y-1.5">
          <div className="text-xs font-semibold text-foreground">{t("generate.panel.compiling")}</div>
          <div className="h-1 overflow-hidden rounded-full bg-primary/15">
            <div className="h-full w-1/3 animate-[shimmer_1.2s_ease-in-out_infinite] rounded-full bg-gradient-to-r from-primary/40 via-primary to-primary/40" />
          </div>
        </div>
      </div>
    );
  }
  if (!result && !signature) return null;
  return (
    <>
      {result && (
        <Alert variant={isErrorString(result) ? "destructive" : "default"} className="mt-3 rounded-xl shadow-sm">
          <AlertDescription>
            <pre className="max-h-48 overflow-y-auto font-mono text-xs leading-5 whitespace-pre-wrap [scrollbar-width:thin]">{result}</pre>
          </AlertDescription>
        </Alert>
      )}
      {hint && (
        <div className="mt-2 rounded-lg border border-hint/20 bg-hint/5 p-3 text-xs text-hint-ink shadow-sm">
          <div className="flex items-center gap-2 mb-1">
            <ShieldAlert className="size-4 text-hint-ink" />
            <span className="font-semibold">{t("generate.build_hint_title", { code: hint.code })}</span>
          </div>
          <pre className="max-h-32 overflow-y-auto font-mono text-[11px] leading-4 whitespace-pre-wrap">{hint.text}</pre>
        </div>
      )}
      <BuildSignatureBadge signature={signature} />
    </>
  );
}

/**
 * BuildStatusBadge — live state chip for the card header:
 * Ready · Compiling · Done · Failed.
 */
export function BuildStatusBadge({ busy, result }: { busy?: boolean; result?: ReactNode }) {
  const { t } = useI18n();
  if (busy) {
    return (
      <Badge variant="warning" className="gap-1 rounded-full px-2.5 py-1 text-xs shadow-sm">
        <Loader2 className="size-3.5 animate-spin" /> {t("generate.badge_compiling")}
      </Badge>
    );
  }
  if (result) {
    return isErrorString(result) ? (
      <Badge variant="destructive" className="gap-1 rounded-full px-2.5 py-1 text-xs shadow-sm">
        <XCircle className="size-3.5" /> {t("generate.badge_failed")}
      </Badge>
    ) : (
      <Badge variant="success" className="gap-1 rounded-full px-2.5 py-1 text-xs shadow-sm">
        <CheckCircle2 className="size-3.5" /> {t("generate.badge_done")}
      </Badge>
    );
  }
  return (
    <Badge variant="outline" className="gap-1 rounded-full px-2.5 py-1 text-xs text-muted-foreground">
      <span className="size-1.5 rounded-full bg-muted-foreground/40" aria-hidden="true" />
      {t("generate.badge_ready")}
    </Badge>
  );
}
