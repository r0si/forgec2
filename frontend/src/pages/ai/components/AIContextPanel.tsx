import { useCallback, useEffect, useRef, useState } from "react";
import { AlertTriangle, Bot, FileText, LoaderCircle, Paperclip, RefreshCw, Shield, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { errorText } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { useErrorToast } from "@/lib/hooks/useErrorToast";
import { normalizeListEnvelope } from "@/lib/envelope";
import { formatBytes } from "@/lib/utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { KnowledgeSearchPanel } from "./KnowledgeSearchPanel";

interface AIProfile {
  id: number;
  name: string;
  provider: string;
  model: string;
  supports_reasoning: boolean;
  supports_tools: boolean;
  is_default: boolean;
}

interface AIAttachment {
  id: string;
  filename: string;
  size: number;
}

interface AIKnowledgeCollection {
  id: number;
  name: string;
  shared: boolean;
}

interface AIContextPanelProps {
  sessionId: number | null;
  profileId: number | null;
  selectedAttachmentIds: string[];
  selectedCollectionIds: number[];
  lowRiskAuto: boolean;
  onProfileChange: (id: number | null) => void;
  onAttachmentIdsChange: (ids: string[]) => void;
  onCollectionIdsChange: (ids: number[]) => void;
  onLowRiskAutoChange: (enabled: boolean) => void;
}

export function AIContextPanel({
  sessionId,
  profileId,
  selectedAttachmentIds,
  selectedCollectionIds,
  lowRiskAuto,
  onProfileChange,
  onAttachmentIdsChange,
  onCollectionIdsChange,
  onLowRiskAutoChange,
}: AIContextPanelProps) {
  const { t } = useI18n();
  const { toastError } = useErrorToast();
  const [profiles, setProfiles] = useState<AIProfile[]>([]);
  const [attachments, setAttachments] = useState<AIAttachment[]>([]);
  const [collections, setCollections] = useState<AIKnowledgeCollection[]>([]);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    try {
      setLoadError(null);
      const [profilePayload, collectionPayload, attachmentPayload] = await Promise.all([
        api.get<unknown>(paths.ai.profiles, { signal }),
        api.get<unknown>(paths.ai.knowledgeCollections, { signal }),
        sessionId == null ? Promise.resolve([] as AIAttachment[]) : api.get<unknown>(paths.ai.attachments(sessionId), { signal }),
      ]);
      if (signal?.aborted) return;
      const profileData = normalizeListEnvelope(profilePayload, ["profiles", "data"]) as AIProfile[];
      const collectionData = normalizeListEnvelope(collectionPayload, ["collections", "data"]) as AIKnowledgeCollection[];
      const attachmentData = normalizeListEnvelope(attachmentPayload, ["attachments", "data"]) as AIAttachment[];
      setProfiles(profileData);
      setCollections(collectionData);
      setAttachments(attachmentData);
      if (profileId == null) {
        const preferred = profileData.find((profile) => profile.is_default) ?? profileData[0];
        if (preferred) onProfileChange(preferred.id);
      }
    } catch (error) {
      if (signal?.aborted || (error instanceof Error && error.name === "AbortError")) return;
      // Retain the last good lists; the banner below only appears when
      // nothing is on screen, so a failed refresh never masquerades as
      // "no profiles / no attachments / no collections".
      const msg = errorText(t, error, () => t("ai.context_load_failed"));
      setLoadError(msg);
      toast.error(msg);
    }
  }, [onProfileChange, profileId, sessionId, t]);

  useEffect(() => {
    const controller = new AbortController();
    void refresh(controller.signal);
    return () => controller.abort();
  }, [refresh]);

  const upload = async (files: FileList | null) => {
    if (!files?.length || sessionId == null) return;
    if (files.length > 5) {
      toast.error(t("ai.attachment_limit"));
      return;
    }
    setUploading(true);
    try {
      const form = new FormData();
      Array.from(files).forEach((file) => form.append("files", file));
      // Central api wrapper: CSRF double-submit, 60s default timeout for
      // uploads, and 401 -> login redirect are handled inside.
      const created = await api.postFormData<AIAttachment[]>(paths.ai.attachments(sessionId), form, { timeout: 60000 });
      setAttachments((current) => [...current, ...created]);
      onAttachmentIdsChange([...new Set([...selectedAttachmentIds, ...created.map((item) => item.id)])]);
      toast.success(t("ai.attachment_saved"));
    } catch (error) {
      toastError(error, "ai.attachment_upload_failed");
    } finally {
      setUploading(false);
      if (fileInputRef.current) fileInputRef.current.value = "";
    }
  };

  const removeAttachment = async (id: string) => {
    try {
	  await api.del(paths.ai.attachment(id));
      setAttachments((current) => current.filter((item) => item.id !== id));
      onAttachmentIdsChange(selectedAttachmentIds.filter((value) => value !== id));
    } catch (error) {
      toastError(error, "ai.attachment_remove_failed");
    }
  };

  const updatePolicy = async (enabled: boolean) => {
    onLowRiskAutoChange(enabled);
    if (sessionId == null) return;
    try {
      await api.putJson(paths.ai.session(sessionId), { write_policy: enabled ? "low_risk_auto" : "approval" });
    } catch (error) {
      onLowRiskAutoChange(!enabled);
      toastError(error, "ai.policy_update_failed");
    }
  };

  return (
    <div className="flex h-full flex-col bg-muted/20">
      <div className="flex items-center gap-2 border-b border-border px-4 py-3">
        <Shield className="size-4 text-primary" />
        <div>
          <h2 className="text-sm font-semibold">{t("ai.context_title")}</h2>
          <p className="text-xs text-muted-foreground">{t("ai.context_subtitle")}</p>
        </div>
        <Button variant="ghost" size="icon-xs" className="ml-auto" onClick={() => void refresh()} aria-label={t("ai.context_refresh")}>
          <RefreshCw className="size-3.5" />
        </Button>
      </div>

      <div className="content-auto min-h-0 flex-1 space-y-5 overscroll-contain overflow-y-auto p-4 scrollbar-thin">
        {loadError && profiles.length === 0 && collections.length === 0 && attachments.length === 0 && (
          <div role="alert" className="flex flex-wrap items-center gap-2 rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning-ink">
            <AlertTriangle className="size-3.5 shrink-0" />
            <span className="min-w-[160px] flex-1">{t("ai.context_unreadable", { message: loadError })}</span>
            <button type="button" className="shrink-0 underline" onClick={() => void refresh()}>
              {t("common.try_again")}
            </button>
          </div>
        )}
        {(() => {
          const used = attachments.filter((a) => selectedAttachmentIds.includes(a.id)).reduce((s, a) => s + a.size, 0) + selectedCollectionIds.length * 8000;
          const budget = 48 * 1024;
          const pct = Math.min(100, Math.round((used / budget) * 100));
          const tone = pct > 95 ? "bg-destructive" : pct > 80 ? "bg-amber-500" : "bg-primary";
          return (
            <section className="space-y-1">
              <div className="flex justify-between text-xs text-muted-foreground"><span>{t("ai.context_budget")}</span><span>{pct}% · {formatBytes(used)}/{formatBytes(budget)}</span></div>
              <div className="h-1.5 w-full rounded-full bg-muted"><div className={`h-1.5 rounded-full ${tone}`} style={{ width: `${pct}%` }} /></div>
              {pct > 80 && <p className="text-xs text-amber-600">{t("ai.context_budget_hint")}</p>}
            </section>
          );
        })()}
        <section className="space-y-2">
          <Label htmlFor="ai-profile"><Bot className="size-3.5" />{t("ai.profile")}</Label>
          <select
            id="ai-profile"
            value={profileId ?? ""}
            onChange={(event) => onProfileChange(event.target.value ? Number(event.target.value) : null)}
            className="h-10 w-full rounded-lg border border-input bg-card px-3 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <option value="">{t("ai.profile_default")}</option>
            {profiles.map((profile) => <option key={profile.id} value={profile.id}>{profile.name} · {profile.model}</option>)}
          </select>
          {profileId && (() => {
            const selected = profiles.find((profile) => profile.id === profileId);
            return selected ? <div className="flex flex-wrap gap-1"><Badge variant="outline">{selected.provider}</Badge>{selected.supports_reasoning && <Badge variant="info">{t("ai.profile_reasoning")}</Badge>}{selected.supports_tools && <Badge variant="outline">{t("ai.profile_tools")}</Badge>}</div> : null;
          })()}
        </section>

        <section className="space-y-2">
          <div className="flex items-center justify-between">
            <Label><Paperclip className="size-3.5" />{t("ai.attachments")}</Label>
            <Button size="xs" variant="outline" disabled={sessionId == null || uploading} onClick={() => fileInputRef.current?.click()}>
              {uploading ? <LoaderCircle className="size-3 animate-spin" /> : <Paperclip className="size-3" />}{t("ai.attachment_upload")}
            </Button>
            <input ref={fileInputRef} className="hidden" type="file" multiple accept=".txt,.md,.log,.json,.yaml,.yml,.csv,.tsv,.go,.js,.ts,.tsx,.jsx,.py,.ps1,.sh,.sql,.xml,.html,.css,.toml,.ini,.conf,.cfg" onChange={(event) => void upload(event.target.files)} />
          </div>
          {sessionId == null && <p className="text-xs text-muted-foreground">{t("ai.attachment_upload_after_session")}</p>}
          <div className="space-y-1.5">
            {attachments.map((attachment) => (
              <div key={attachment.id} className="flex items-center gap-2 rounded-lg border border-border bg-card p-2 text-xs">
                <input type="checkbox" checked={selectedAttachmentIds.includes(attachment.id)} onChange={(event) => onAttachmentIdsChange(event.target.checked ? [...selectedAttachmentIds, attachment.id] : selectedAttachmentIds.filter((id) => id !== attachment.id))} aria-label={t("ai.attachment_use", { name: attachment.filename })} />
                <FileText className="size-3.5 text-muted-foreground" />
                <span className="min-w-0 flex-1 truncate" title={attachment.filename}>{attachment.filename}</span>
                <span className="text-muted-foreground">{formatBytes(attachment.size)}</span>
                <Button variant="ghost" size="icon-xs" className="text-muted-foreground hover:text-destructive-ink" onClick={() => void removeAttachment(attachment.id)} aria-label={t("ai.attachment_remove", { name: attachment.filename })}><Trash2 className="size-3" /></Button>
              </div>
            ))}
          </div>
        </section>

        <section className="space-y-2">
          <Label>{t("ai.knowledge_collections")}</Label>
          {collections.length === 0 ? <p className="text-xs text-muted-foreground">{t("ai.knowledge_empty")}</p> : collections.map((collection) => (
            <label key={collection.id} className="flex min-h-9 items-center gap-2 rounded-lg border border-border bg-card px-3 text-xs">
              <input type="checkbox" checked={selectedCollectionIds.includes(collection.id)} onChange={(event) => onCollectionIdsChange(event.target.checked ? [...selectedCollectionIds, collection.id] : selectedCollectionIds.filter((id) => id !== collection.id))} />
              <span className="min-w-0 flex-1 truncate">{collection.name}</span>
              {collection.shared && <Badge variant="outline">{t("ai.knowledge_shared")}</Badge>}
            </label>
          ))}
        </section>

        <KnowledgeSearchPanel selectedCollectionIds={selectedCollectionIds} />

        <section className="rounded-xl border border-border bg-card p-3">
          <label className="flex cursor-pointer items-start gap-3">
            <input type="checkbox" className="mt-1" checked={lowRiskAuto} onChange={(event) => void updatePolicy(event.target.checked)} />
            <span><span className="block text-sm font-medium">{t("ai.low_risk_auto")}</span><span className="mt-0.5 block text-xs leading-5 text-muted-foreground">{t("ai.low_risk_auto_hint")}</span></span>
          </label>
        </section>
      </div>
    </div>
  );
}
