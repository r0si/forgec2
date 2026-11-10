import { registerFragment } from "@/lib/i18n";
import { enProfiles } from "@/lib/i18n/fragments/en.profiles";
registerFragment(enProfiles);

import { useCallback, useEffect, useMemo, useRef, useState, type ChangeEvent } from "react";
import { Link } from "react-router-dom";
import {
  AlertTriangle,
  CheckCircle2,
  Code2,
  Copy,
  Download,
  FileCode2,
  FileDown,
  FileWarning,
  List,
  Plus,
  RotateCcw,
  Save,
  Search,
  Settings,
  Trash2,
  Upload,
  XCircle,
} from "lucide-react";
import { toast } from "sonner";
import { PageContainer } from "@/components/ui/page-container";
import { CardHeaderRow } from "@/components/ui/card-header-row";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DataError } from "@/components/ui/data-state";
import { EmptyState } from "@/components/ui/empty-state";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { useI18n } from "@/lib/i18n";
import { downloadText } from "@/lib/download";
import { formatThrownError } from "@/lib/api";
import { cn } from "@/lib/utils";
import { ProfileEditor, type ProfileEditorHandle } from "./components/ProfileEditor";
import { starterProfile } from "./components/cs-language";
import { useProfilesData } from "./components/useProfilesData";
import type {
  AgentProfile,
  CompiledProfileSummary,
  ProfileDiagnostic,
} from "./components/types";

const PROFILE_NAME_RE = /^[A-Za-z0-9_-]+$/;

function uniqueProfileName(base: string, profiles: AgentProfile[]): string {
  const cleaned = base.replace(/[^A-Za-z0-9_-]/g, "_").replace(/^_+|_+$/g, "") || "profile";
  const names = new Set(profiles.map((p) => p.name));
  if (!names.has(cleaned)) return cleaned;
  let index = 2;
  while (names.has(`${cleaned}_${index}`)) index += 1;
  return `${cleaned}_${index}`;
}

function draftProfile(name: string, source: string): AgentProfile {
  return {
    name,
    description: "",
    user_agent: "",
    beacon_uri: "",
    method: "POST",
    headers: {},
    sleep: 0,
    jitter: 0,
    source,
    builtin: false,
    draft: true,
  };
}

function profileNameFromFile(filename: string): string {
  return filename
    .replace(/\.(profile|json|txt)$/i, "")
    .replace(/[^A-Za-z0-9_-]/g, "_")
    .replace(/^_+|_+$/g, "") || "imported";
}

function fallbackSummary(profile: AgentProfile | undefined): CompiledProfileSummary | null {
  if (!profile) return null;
  const uris = profile.beacon_uris?.length
    ? profile.beacon_uris
    : profile.uris?.length
      ? profile.uris
      : profile.beacon_uri
        ? [profile.beacon_uri]
        : [];
  return {
    name: profile.name,
    method: profile.method,
    uris,
    sleep: profile.sleep,
    jitter: profile.jitter,
    user_agent: profile.user_agent,
    headers: profile.headers,
    client_metadata: profile.client_metadata,
    client_id: profile.client_id,
    server_output: profile.server_output,
    parameter: profile.parameter,
    content_length_jitter: profile.content_length_jitter,
    jitter_uri: profile.jitter_uri,
    jitter_parameter: profile.jitter_parameter,
  };
}

function SummaryField({
  label,
  value,
  mono = false,
  className,
}: {
  label: string;
  value: string | number | undefined | null;
  mono?: boolean;
  className?: string;
}) {
  const shown = value === undefined || value === null || value === "" ? "—" : String(value);
  return (
    <div className={cn("min-w-0 rounded-md border border-border/70 bg-background/50 px-3 py-2", className)}>
      <div className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className={cn("mt-0.5 break-words text-sm", mono && "font-mono text-xs")}>{shown}</div>
    </div>
  );
}

function DiagnosticIcon({ severity }: { severity: ProfileDiagnostic["severity"] }) {
  return severity === "error"
    ? <XCircle className="mt-0.5 size-3.5 shrink-0 text-destructive" />
    : <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-warning-ink" />;
}

export default function ProfilesPage({ embedded = false }: { embedded?: boolean }) {
  const { t } = useI18n();
  const { confirm, modal } = useConfirm();
  const {
    profiles,
    setProfiles,
    selectedIdx,
    setSelectedIdx,
    source,
    setSource,
    savedSource,
    setSavedSource,
    dirty,
    loadingProfiles,
    profilesError,
    validating,
    validation,
    setValidation,
    selectProfile,
    loadProfiles,
    validate,
    save,
    remove,
    importFile,
  } = useProfilesData();

  const [name, setName] = useState("");
  const [search, setSearch] = useState("");
  const [saving, setSaving] = useState(false);
  const [importing, setImporting] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const editorRef = useRef<ProfileEditorHandle | null>(null);
  const fileInputRef = useRef<HTMLInputElement | null>(null);

  const selectedProfile = selectedIdx >= 0 ? profiles[selectedIdx] : undefined;
  const diagnostics = useMemo(() => validation?.diagnostics ?? [], [validation?.diagnostics]);
  const diagnosticErrors = diagnostics.filter((d) => d.severity === "error");
  const hasErrors = validation?.valid === false || diagnosticErrors.length > 0;
  const warningMessages = useMemo(() => {
    const seen = new Set<string>();
    return (validation?.warnings ?? []).filter((message) => {
      if (diagnostics.some((d) => d.severity === "warning" && d.message === message)) return false;
      if (seen.has(message)) return false;
      seen.add(message);
      return true;
    });
  }, [validation?.warnings, diagnostics]);
  const preview = validation?.compiled ?? fallbackSummary(selectedProfile);
  const nameValid = PROFILE_NAME_RE.test(name);
  const builtinNameUnchanged = Boolean(selectedProfile?.builtin && name === selectedProfile.name);
  const canSave = Boolean(source.trim()) && nameValid && !saving && !builtinNameUnchanged;
  const sourceEdited = source !== savedSource;
  const filteredProfiles = useMemo(() => {
    const needle = search.trim().toLowerCase();
    return profiles
      .map((profile, index) => ({ profile, index }))
      .filter(({ profile }) => {
        if (!needle) return true;
        return profile.name.toLowerCase().includes(needle) ||
          (profile.description || "").toLowerCase().includes(needle);
      });
  }, [profiles, search]);

  useEffect(() => {
    if (selectedProfile) setName(selectedProfile.name);
    // Only re-seed the editable name when the selected profile changes, not on
    // every keystroke.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedIdx, selectedProfile?.name]);

  const runValidation = useCallback(async (candidateName: string, content: string, silent = false) => {
    try {
      return await validate(candidateName || "draft", content);
    } catch (e) {
      if (!silent && content.trim()) toast.error(formatThrownError(e));
      return null;
    }
  }, [validate]);

  // Live validation is debounced: operators can type freely without a request
  // per keystroke, while syntax errors still appear without pressing Validate.
  useEffect(() => {
    if (!source.trim() || loadingProfiles) return;
    const timer = window.setTimeout(() => {
      void runValidation(name, source, true);
    }, 900);
    return () => window.clearTimeout(timer);
  }, [name, source, loadingProfiles, runValidation]);

  useEffect(() => {
    if (!dirty) return;
    const handler = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [dirty]);

  const discardIfNeeded = useCallback(async () => {
    if (!dirty) return true;
    return confirm({
      title: t("profiles.discard_title"),
      message: t("profiles.discard"),
      danger: true,
      confirmText: t("profiles.discard_confirm"),
    });
  }, [confirm, dirty, t]);

  const handleSelect = async (index: number) => {
    if (index === selectedIdx) return;
    if (!(await discardIfNeeded())) return;
    let nextProfiles = profiles;
    let nextIndex = index;
    if (selectedProfile?.draft) {
      nextProfiles = profiles.filter((_, i) => i !== selectedIdx);
      nextIndex = index > selectedIdx ? index - 1 : index;
      setProfiles(nextProfiles);
    }
    selectProfile(nextIndex, nextProfiles);
    const next = nextProfiles[nextIndex];
    if (next) setName(next.name);
  };

  const handleNew = async () => {
    if (!(await discardIfNeeded())) return;
    let base = profiles;
    if (selectedProfile?.draft) {
      base = profiles.filter((_, index) => index !== selectedIdx);
    }
    const draftName = uniqueProfileName("new_profile", base);
    const nextSource = starterProfile(draftName);
    const draft = draftProfile(draftName, nextSource);
    const nextProfiles = [...base, draft];
    setProfiles(nextProfiles);
    setSelectedIdx(nextProfiles.length - 1);
    setSource(nextSource);
    setSavedSource("");
    setValidation(null);
    setName(draftName);
    setSearch("");
    window.setTimeout(() => editorRef.current?.focus(), 0);
  };

  const handleDuplicate = async () => {
    if (!source.trim()) {
      toast.error(t("profiles.empty_editor"));
      return;
    }
    let base = profiles;
    if (selectedProfile?.draft) {
      base = profiles.filter((_, index) => index !== selectedIdx);
    }
    const draftName = uniqueProfileName(`${name || selectedProfile?.name || "profile"}_copy`, base);
    const draft = draftProfile(draftName, source);
    const nextProfiles = [...base, draft];
    setProfiles(nextProfiles);
    setSelectedIdx(nextProfiles.length - 1);
    setSource(source);
    setSavedSource("");
    setValidation(null);
    setName(draftName);
    window.setTimeout(() => editorRef.current?.focus(), 0);
  };

  const handleSave = async () => {
    if (!source.trim()) {
      toast.error(t("profiles.empty_editor"));
      return;
    }
    if (!nameValid) {
      toast.error(t("profiles.name_invalid"));
      return;
    }
    if (builtinNameUnchanged) {
      toast.error(t("profiles.builtin_read_only"));
      return;
    }
    if (profiles.some((profile) => profile.builtin && profile.name === name)) {
      toast.error(t("profiles.builtin_read_only"));
      return;
    }
    const target = profiles.find((profile) => !profile.builtin && profile.name === name);
    if (target && target.name !== selectedProfile?.name) {
      const overwrite = await confirm({
        title: t("profiles.overwrite_title"),
        message: t("profiles.overwrite_confirm", { name }),
        danger: true,
      });
      if (!overwrite) return;
    }
    setSaving(true);
    try {
      const check = await runValidation(name, source);
      if (!check) return;
      if (check.valid === false || (check.diagnostics ?? []).some((d) => d.severity === "error")) {
        toast.error(t("profiles.fix_errors_first"));
        return;
      }
      const result = await save(name, source);
      const savedName = result.profile?.name || name;
      setName(savedName);
      if (check) setValidation(check);
      toast.success(t("profiles.save_success"));
    } catch (e) {
      toast.error(formatThrownError(e) || t("profiles.save_failed"));
    } finally {
      setSaving(false);
    }
  };

  const handleValidate = async () => {
    if (!source.trim()) {
      toast.error(t("profiles.empty_editor"));
      return;
    }
    const result = await runValidation(name, source);
    if (!result) return;
    if (result.valid === false || (result.diagnostics ?? []).some((d) => d.severity === "error")) {
      toast.error(t("profiles.invalid"));
    } else if ((result.warnings ?? []).length > 0) {
      toast.warning(t("profiles.validate_warnings", { count: (result.warnings ?? []).length }));
    } else {
      toast.success(t("profiles.validate_ok"));
    }
  };

  const handleDelete = async () => {
    if (!selectedProfile) return;
    if (selectedProfile.builtin) {
      toast.error(t("profiles.delete_builtin"));
      return;
    }
    const isDraft = Boolean(selectedProfile.draft);
    const ok = await confirm({
      title: t("profiles.delete_title"),
      message: isDraft
        ? t("profiles.discard_draft", { name: selectedProfile.name })
        : t("profiles.delete_confirm", { name: selectedProfile.name }),
      danger: true,
    });
    if (!ok) return;
    if (isDraft) {
      const nextProfiles = profiles.filter((_, index) => index !== selectedIdx);
      setProfiles(nextProfiles);
      if (nextProfiles.length === 0) {
        setSelectedIdx(-1);
        setSource("");
        setSavedSource("");
        setValidation(null);
        setName("");
      } else {
        const nextIdx = Math.min(selectedIdx, nextProfiles.length - 1);
        setSelectedIdx(nextIdx);
        setSource(nextProfiles[nextIdx].source || "");
        setSavedSource(nextProfiles[nextIdx].source || "");
        setValidation(null);
        setName(nextProfiles[nextIdx].name);
      }
      return;
    }
    setDeleting(true);
    try {
      const nextProfiles = await remove(selectedProfile.name);
      const nextIdx = nextProfiles.length > 0 ? Math.min(selectedIdx, nextProfiles.length - 1) : -1;
      setName(nextIdx >= 0 ? nextProfiles[nextIdx].name : "");
      toast.success(t("profiles.toast.profile_removed", { name: selectedProfile.name }));
    } catch (e) {
      toast.error(formatThrownError(e) || t("profiles.toast.delete_failed"));
    } finally {
      setDeleting(false);
    }
  };

  const handleFileInput = async (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file) return;
    if (file.size > 5 * 1024 * 1024) {
      toast.error(t("profiles.file_too_large"));
      return;
    }
    const isJSON = file.name.toLowerCase().endsWith(".json");
    if (isJSON) {
      if (!(await discardIfNeeded())) return;
      setImporting(true);
      try {
        const result = await importFile(file);
        setName(result.profile?.name || "");
        toast.success(t("profiles.toast.profile_imported"));
      } catch (e) {
        toast.error(formatThrownError(e) || t("profiles.toast.import_failed"));
      } finally {
        setImporting(false);
      }
      return;
    }
    if (!(await discardIfNeeded())) return;
    try {
      const text = await file.text();
      let base = profiles;
      if (selectedProfile?.draft) {
        base = profiles.filter((_, index) => index !== selectedIdx);
      }
      const draftName = uniqueProfileName(profileNameFromFile(file.name), base);
      const draft = draftProfile(draftName, text);
      const nextProfiles = [...base, draft];
      setProfiles(nextProfiles);
      setSelectedIdx(nextProfiles.length - 1);
      setSource(text);
      setSavedSource("");
      setValidation(null);
      setName(draftName);
      setSearch("");
      toast.success(t("profiles.imported_draft"));
      window.setTimeout(() => editorRef.current?.focus(), 0);
    } catch (e) {
      toast.error(formatThrownError(e) || t("profiles.toast.import_failed"));
    }
  };

  const handleRevert = () => {
    if (!selectedProfile || selectedProfile.draft) return;
    setSource(savedSource);
    setValidation(null);
  };

  const handleDownload = () => {
    if (!source.trim()) {
      toast.error(t("profiles.empty_editor"));
      return;
    }
    const filename = `${name || "profile"}.profile`;
    downloadText(source, filename);
  };

  const handleCopy = async () => {
    if (!source.trim()) {
      toast.error(t("profiles.empty_editor"));
      return;
    }
    try {
      await navigator.clipboard.writeText(source);
      toast.success(t("profiles.copied"));
    } catch {
      toast.error(t("profiles.copy_failed"));
    }
  };

  const statusBadge = validating ? (
    <Badge variant="secondary" className="gap-1">
      <Spinner size="xs" /> {t("profiles.validating")}
    </Badge>
  ) : !validation ? (
    <Badge variant="outline">{t("profiles.not_validated")}</Badge>
  ) : hasErrors ? (
    <Badge variant="destructive" className="gap-1"><XCircle className="size-3" /> {t("profiles.invalid")}</Badge>
  ) : warningMessages.length > 0 ? (
    <Badge variant="warning" className="gap-1"><AlertTriangle className="size-3" /> {t("profiles.valid_warnings")}</Badge>
  ) : (
    <Badge variant="success" className="gap-1"><CheckCircle2 className="size-3" /> {t("profiles.valid")}</Badge>
  );

  const listBody = loadingProfiles ? (
    <div className="flex items-center justify-center py-14"><Spinner size="sm" /></div>
  ) : profilesError ? (
    <DataError message={profilesError} onRetry={() => { void loadProfiles(); }} className="py-8" />
  ) : filteredProfiles.length === 0 ? (
    <div className="px-3 py-12 text-center text-xs text-muted-foreground">
      <FileWarning className="mx-auto mb-2 size-5" />
      {search ? t("profiles.no_match") : t("profiles.none_loaded")}
    </div>
  ) : (
    <div className="max-h-[560px] space-y-1 overflow-y-auto pr-1">
      {filteredProfiles.map(({ profile, index }) => {
        const active = index === selectedIdx;
        return (
          <button
            key={`${profile.name}-${index}`}
            type="button"
            onClick={() => { void handleSelect(index); }}
            className={cn(
              "w-full rounded-lg border px-3 py-2 text-left transition-colors",
              active
                ? "border-primary/40 bg-primary/10 text-foreground"
                : "border-transparent hover:border-border hover:bg-muted/50",
            )}
          >
            <div className="flex min-w-0 items-center gap-2">
              <span className="truncate text-sm font-medium">{profile.name}</span>
              {profile.builtin && <Badge variant="secondary" className="ml-auto">{t("profiles.builtin")}</Badge>}
              {profile.draft && <Badge variant="warning" className="ml-auto">{t("profiles.draft")}</Badge>}
              {active && dirty && !profile.builtin && !profile.draft && (
                <span className="ml-auto size-1.5 shrink-0 rounded-full bg-warning" aria-hidden="true" />
              )}
            </div>
            {profile.description && (
              <p className="mt-0.5 truncate text-xs text-muted-foreground">{profile.description}</p>
            )}
            <p className="mt-1 truncate font-mono text-[11px] text-muted-foreground">
              {profile.method || "POST"} {profile.beacon_uri || ""}
            </p>
          </button>
        );
      })}
    </div>
  );

  return (
    <PageContainer
      embedded={embedded}
      title={!embedded ? t("profiles.title") : undefined}
      subtitle={!embedded ? t("profiles.subtitle") : undefined}
      className="space-y-4"
    >
      <div className="flex flex-wrap items-center gap-3 rounded-lg border border-border bg-muted/30 px-4 py-3">
        <Code2 className="size-4 shrink-0 text-primary" />
        <p className="min-w-0 flex-1 text-sm text-muted-foreground">{t("profiles.editor_hint")}</p>
        <Button variant="outline" size="sm" render={<Link to="/settings?tab=malleable" />}>
          <Settings className="size-4" /> {t("profiles.malleable_open_settings")}
        </Button>
      </div>

      <div className="grid gap-4 xl:grid-cols-[minmax(240px,300px)_minmax(0,1fr)]">
        <Card className="overflow-hidden self-start">
          <CardHeaderRow
            icon={List}
            tone="primary"
            title={t("profiles.list_title")}
            description={t("profiles.list_count", { count: profiles.length })}
          />
          <CardContent className="space-y-3 p-3 sm:p-4">
            <div className="relative">
              <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder={t("profiles.search_profiles")}
                aria-label={t("profiles.search_profiles")}
                className="pl-9"
              />
            </div>
            <div className="flex gap-2">
              <Button variant="outline" size="sm" className="flex-1" onClick={() => { void handleNew(); }}>
                <Plus className="size-4" /> {t("profiles.new_profile")}
              </Button>
              <Button
                variant="outline"
                size="sm"
                className="flex-1"
                disabled={importing}
                onClick={() => fileInputRef.current?.click()}
              >
                {importing ? <Spinner size="xs" /> : <Upload className="size-4" />} {t("profiles.import_short")}
              </Button>
            </div>
            <input
              ref={fileInputRef}
              type="file"
              accept=".profile,.txt,.json,application/json"
              className="hidden"
              onChange={(event) => { void handleFileInput(event); }}
            />
            {listBody}
          </CardContent>
        </Card>

        <Card className="overflow-hidden">
          <CardHeaderRow
            icon={FileCode2}
            tone="violet"
            title={name || selectedProfile?.name || t("profiles.editor_title")}
            description={t("profiles.editor_desc")}
            action={statusBadge}
          />
          <CardContent className="space-y-3 p-3 sm:p-4">
            {!selectedProfile && !loadingProfiles ? (
              <EmptyState
                icon={FileCode2}
                title={t("profiles.empty_title")}
                message={t("profiles.empty_message")}
                action={<Button onClick={() => { void handleNew(); }}><Plus className="size-4" /> {t("profiles.new_profile")}</Button>}
              />
            ) : (
              <>
                <div className="flex flex-col gap-3 lg:flex-row lg:items-start">
                  <div className="min-w-0 flex-1">
                    <label className="mb-1 block text-xs font-medium text-muted-foreground" htmlFor="profile-name">
                      {t("profiles.profile_name_label")}
                    </label>
                    <Input
                      id="profile-name"
                      value={name}
                      onChange={(event) => setName(event.target.value)}
                      className="font-mono"
                      aria-invalid={!nameValid}
                      spellCheck={false}
                    />
                    {!nameValid ? (
                      <p className="mt-1 text-xs text-destructive">{t("profiles.name_invalid")}</p>
                    ) : builtinNameUnchanged ? (
                      <p className="mt-1 text-xs text-muted-foreground">{t("profiles.builtin_read_only")}</p>
                    ) : null}
                  </div>
                  <div className="flex flex-wrap gap-2 lg:justify-end">
                    <Button size="sm" onClick={() => { void handleSave(); }} disabled={!canSave}>
                      {saving ? <Spinner size="xs" /> : <Save className="size-4" />}
                      {selectedProfile?.builtin || (selectedProfile && name !== selectedProfile.name)
                        ? t("profiles.save_as")
                        : t("profiles.save")}
                    </Button>
                    <Button size="sm" variant="outline" onClick={() => { void handleValidate(); }} disabled={validating || !source.trim()}>
                      {validating ? <Spinner size="xs" /> : <CheckCircle2 className="size-4" />} {t("profiles.validate")}
                    </Button>
                    <Button size="sm" variant="outline" onClick={handleRevert} disabled={!sourceEdited || Boolean(selectedProfile?.draft)}>
                      <RotateCcw className="size-4" /> {t("profiles.revert")}
                    </Button>
                    <Button size="sm" variant="outline" onClick={handleDownload} disabled={!source.trim()}>
                      <Download className="size-4" /> {t("profiles.download")}
                    </Button>
                    <Button size="sm" variant="outline" onClick={() => { void handleCopy(); }} disabled={!source.trim()}>
                      <Copy className="size-4" /> {t("profiles.copy")}
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => { void handleDuplicate(); }}
                      disabled={!source.trim()}
                    >
                      <FileDown className="size-4" /> {t("profiles.duplicate")}
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      className="text-destructive hover:text-destructive"
                      onClick={() => { void handleDelete(); }}
                      disabled={!selectedProfile || Boolean(selectedProfile.builtin) || deleting}
                    >
                      {deleting ? <Spinner size="xs" /> : <Trash2 className="size-4" />} {t("profiles.delete")}
                    </Button>
                  </div>
                </div>

                <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                  {dirty ? <Badge variant="warning">{t("profiles.dirty")}</Badge> : <Badge variant="outline">{t("profiles.saved")}</Badge>}
                  {selectedProfile?.builtin && <Badge variant="secondary">{t("profiles.builtin")}</Badge>}
                  {selectedProfile?.draft && <Badge variant="warning">{t("profiles.draft")}</Badge>}
                  <span className="ml-auto font-mono">{source.split("\n").length} lines</span>
                </div>

                <ProfileEditor
                  ref={editorRef}
                  value={source}
                  onChange={setSource}
                  diagnostics={diagnostics}
                  onSave={() => { void handleSave(); }}
                  ariaLabel={t("profiles.editor_aria")}
                  placeholderText={t("profiles.editor_placeholder")}
                />

                {diagnostics.length > 0 && (
                  <div className="overflow-hidden rounded-lg border border-border">
                    <div className="flex items-center gap-2 border-b border-border bg-muted/40 px-3 py-2 text-xs font-medium">
                      <FileWarning className="size-4 text-warning-ink" />
                      {t("profiles.problems")}
                      <Badge variant={diagnosticErrors.length > 0 ? "destructive" : "warning"} className="ml-auto">
                        {diagnostics.length}
                      </Badge>
                    </div>
                    <div className="max-h-44 divide-y divide-border overflow-y-auto">
                      {diagnostics.map((diagnostic, index) => (
                        <button
                          key={`${diagnostic.line}-${diagnostic.column}-${diagnostic.code}-${index}`}
                          type="button"
                          onClick={() => editorRef.current?.goToLine(diagnostic.line, diagnostic.column)}
                          className="flex w-full items-start gap-2 px-3 py-2 text-left text-xs hover:bg-muted/40"
                        >
                          <DiagnosticIcon severity={diagnostic.severity} />
                          <span className="shrink-0 font-mono text-muted-foreground">
                            {diagnostic.line}:{diagnostic.column}
                          </span>
                          <span className="min-w-0 break-words">{diagnostic.message}</span>
                        </button>
                      ))}
                    </div>
                  </div>
                )}

                {warningMessages.length > 0 && (
                  <div className="rounded-lg border border-warning/30 bg-warning/10 px-3 py-2">
                    <div className="mb-1 flex items-center gap-2 text-xs font-medium text-warning-ink">
                      <AlertTriangle className="size-4" /> {t("profiles.warnings")} ({warningMessages.length})
                    </div>
                    <ul className="max-h-32 space-y-1 overflow-y-auto text-xs text-warning-ink/90">
                      {warningMessages.map((message, index) => (
                        <li key={`${message}-${index}`} className="break-words">• {message}</li>
                      ))}
                    </ul>
                  </div>
                )}

                <div className="rounded-lg border border-border bg-muted/20 p-3">
                  <div className="mb-2 flex flex-wrap items-baseline gap-2">
                    <span className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">{t("profiles.compiled")}</span>
                    <span className="text-xs text-muted-foreground">{t("profiles.compiled_desc")}</span>
                  </div>
                  {!preview ? (
                    <p className="text-xs text-muted-foreground">{t("profiles.not_validated")}</p>
                  ) : (
                    <>
                      <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4">
                        <SummaryField label={t("profiles.method")} value={preview.method || "POST"} />
                        <SummaryField label={t("profiles.sleep_jitter")} value={`${preview.sleep ?? 0}s / ${preview.jitter ?? 0}%`} />
                        <SummaryField label={t("profiles.parameter")} value={preview.parameter} mono />
                        <SummaryField label={t("profiles.content_jitter")} value={preview.content_length_jitter ?? 0} />
                      </div>
                      <div className="mt-2 grid gap-2 sm:grid-cols-2">
                        <SummaryField label={t("profiles.uris")} value={(preview.uris ?? []).join(", ")} mono />
                        <SummaryField label={t("profiles.user_agent")} value={preview.user_agent} mono />
                        <SummaryField label={t("profiles.client_metadata")} value={preview.client_metadata} mono />
                        <SummaryField label={t("profiles.client_id")} value={preview.client_id} mono />
                        <SummaryField label={t("profiles.server_output")} value={preview.server_output} mono />
                        <SummaryField
                          label={t("profiles.flags")}
                          value={[preview.jitter_uri ? "jitter_uri" : "", preview.jitter_parameter ? "jitter_parameter" : ""].filter(Boolean).join(", ")}
                          mono
                        />
                      </div>
                      {preview.ignored && preview.ignored.length > 0 && (
                        <div className="mt-2 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                          <span>{t("profiles.ignored")}:</span>
                          {preview.ignored.map((block) => <Badge key={block} variant="outline">{block}</Badge>)}
                        </div>
                      )}
                    </>
                  )}
                </div>
              </>
            )}
          </CardContent>
        </Card>
      </div>
      {modal}
    </PageContainer>
  );
}
