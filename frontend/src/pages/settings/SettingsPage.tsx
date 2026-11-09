import { registerFragment } from "@/lib/i18n";
import { enSettings } from "@/lib/i18n/fragments/en.settings";
registerFragment(enSettings);

import { useState, useEffect, lazy, Suspense } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { downloadBlob } from "@/lib/download";
import { useI18n } from "@/lib/i18n";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { useErrorToast } from "@/lib/hooks/useErrorToast";
import { useMutation } from "@/lib/hooks/useMutation";
import { PageContainer } from "@/components/ui/page-container";
import { ErrorState } from "@/components/ui/error-state";
import { Permission } from "@/components/ui/permission";
import { PageSpinner } from "@/components/ui/spinner";
import { DataError } from "@/components/ui/data-state";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { SearchInput } from "@/components/SearchInput";
import { Button } from "@/components/ui/button";
import { toast } from "sonner";
import { Bell, Bot, Cpu, Database, FileCode, Lock, Palette, Server, Shield, Wrench, Archive, Radio, AlertTriangle, ScanSearch, SearchX } from "lucide-react";
import { useSettingsData } from "./components/useSettingsData";
import AppearanceSection from "./components/AppearanceSection";
import SecuritySection from "./components/SecuritySection";
import ApiKeysSection from "./components/ApiKeysSection";
import ServerSection from "./components/ServerSection";
import AgentSection from "./components/AgentSection";
import { openUpdateDialog } from "@/components/UpdateDialog";


const MalleableSection = lazy(() => import("./components/MalleableSection"));
const DatabaseSection = lazy(() => import("./components/DatabaseSection"));
const MaintenanceSection = lazy(() => import("./components/MaintenanceSection"));
const AboutSection = lazy(() => import("./components/AboutSection"));
const NotificationsSection = lazy(() => import("./components/NotificationsSection"));
const BackupSection = lazy(() => import("./components/BackupSection"));
const ExtC2Section = lazy(() => import("./components/ExtC2Section"));
const SIEMRulesSection = lazy(() => import("./components/SIEMRulesSection"));
const CertificatesSection = lazy(() => import("./components/CertificatesSection"));
const ModulesSection = lazy(() => import("./components/ModulesSection"));
const EmergencySection = lazy(() => import("./components/EmergencySection"));
const ReloadStatusCard = lazy(() => import("./components/ReloadStatusCard"));

// Keep hash navigation aligned with the actual tab values below. These legacy
// aliases are retained so bookmarks from older releases never land on a blank
// panel after the settings groups were consolidated.
const SETTINGS_SECTION_KEYS = new Set([
  "appearance", "security", "server", "agent", "malleable", "database", "backup",
  "maintenance", "notifications", "extc2", "siem", "certificates", "modules", "emergency", "about",
]);

const SETTINGS_SECTION_ALIASES: Record<string, string> = {
  profile: "appearance",
  theme: "appearance",
  language: "appearance",
  access: "security",
  telemetry: "about",
};

function sectionFromHash(): string | null {
  const raw = window.location.hash.match(/^#tab=(.+)$/)?.[1];
  if (!raw) return null;
  try {
    const section = decodeURIComponent(raw);
    const canonical = SETTINGS_SECTION_ALIASES[section] ?? section;
    return SETTINGS_SECTION_KEYS.has(canonical) ? canonical : null;
  } catch {
    return null;
  }
}

export default function SettingsPage() {
  const { t, setLocale } = useI18n();
  const {
    data,
    loading,
    error: settingsError,
    loaded: settingsLoaded,
    loadSettings,
    agentForm,
    setAgentForm,
    serverForm,
    setServerForm,
    malleableForm,
    setMalleableForm,
    passwordForm,
    setPasswordForm,
    theme,
    setTheme,
    language,
    setLanguage,
  } = useSettingsData();
  const [activeSection, setActiveSection] = useState("appearance");
  const [sectionQuery, setSectionQuery] = useState("");
  const [purgeDays, setPurgeDays] = useState({ tasks: "30", audit: "30" });
  const { confirm: confirmAction, modal: modalAction } = useConfirm();
  const { confirm: confirmPurge, modal: modalPurge } = useConfirm();
  const { toastError } = useErrorToast();

  useEffect(() => {
    const syncFromHash = () => {
      const section = sectionFromHash();
      if (section) setActiveSection(section);
    };
    syncFromHash();
    window.addEventListener("hashchange", syncFromHash);
    return () => window.removeEventListener("hashchange", syncFromHash);
  }, []);

  const { mutate: saveAgent, isPending: savingAgent } = useMutation({
    fn: async (e: React.FormEvent) => {
      e.preventDefault();
      await api.post(paths.settings.agent, { interval: String(agentForm.interval), jitter: String(agentForm.jitter), skip_tls: String(agentForm.skip_tls), user_agent: agentForm.user_agent, working_start: agentForm.working_start, working_end: agentForm.working_end, working_tz: agentForm.working_tz, beacon_randomization: agentForm.beacon_randomization, max_random_interval_ms: String(agentForm.max_random_interval_ms) });
    },
    onSuccess: () => toast.success(t("settings.toast.agent_saved")),
    onError: (e) => toastError(e, "settings.toast.save_failed"),
  });

  const { mutate: saveServer, isPending: savingServer } = useMutation({
    fn: async (e: React.FormEvent) => {
      e.preventDefault();
      await api.post(paths.settings.server, {
        log_level: serverForm.log_level, tcp_enabled: String(serverForm.tcp_enabled), tcp_addr: serverForm.tcp_addr,
        offline_threshold: String(serverForm.offline_threshold), session_max_age: String(serverForm.session_max_age), cleanup_retention: String(serverForm.cleanup_retention),
      });
    },
    onSuccess: () => toast.success(t("settings.toast.server_saved")),
    onError: (e) => toastError(e, "settings.toast.save_failed"),
  });

  const { mutate: saveMalleable, isPending: savingMalleable } = useMutation({
    fn: async (e: React.FormEvent) => {
      e.preventDefault();
      await api.post(paths.settings.malleable, {
        enabled: String(malleableForm.enabled), status_code: String(malleableForm.status_code), content_type: malleableForm.content_type,
        headers_text: malleableForm.headers_text, prepend: malleableForm.prepend, append: malleableForm.append,
      });
    },
    onSuccess: () => toast.success(t("settings.toast.malleable_saved")),
    onError: (e) => toastError(e, "settings.toast.save_failed"),
  });

  const handleSaveAgent = (e: React.FormEvent) => { void saveAgent(e); };
  const handleSaveServer = (e: React.FormEvent) => { void saveServer(e); };
  const handleSaveMalleable = (e: React.FormEvent) => { void saveMalleable(e); };

  const { mutate: changePassword, isPending: savingPassword } = useMutation({
    fn: () => api.post(paths.settings.password, { current_password: passwordForm.current, new_password: passwordForm.next, confirm_password: passwordForm.confirm }),
    onSuccess: () => {
      toast.success(t("settings.toast.password_changed"));
      setPasswordForm({ current: "", next: "", confirm: "" });
    },
    onError: (e) => toastError(e, "settings.toast.password_change_failed"),
  });

  const handleChangePassword = (e: React.FormEvent) => {
    e.preventDefault();
    if (passwordForm.next !== passwordForm.confirm) { toast.error(t("settings.toast.password_mismatch")); return; }
    void changePassword();
  };

  const { mutate: regenerateJWT, isPending: savingJwt } = useMutation({
    fn: () => api.post(paths.settings.jwtRegenerate),
    onSuccess: () => {
      toast.success(t("settings.toast.jwt_regenerated"));
      loadSettings();
    },
    onError: (e) => toastError(e, "settings.toast.jwt_failed"),
  });

  const handleRegenerateJWT = async () => {
    if (!(await confirmAction({ message: t("settings.confirm.jwt") }))) return;
    void regenerateJWT();
  };

  const handleApplyTheme = (th: string) => {
    setTheme(th);
    try {
      localStorage.setItem("forgec2_theme", th);
      const root = document.documentElement;
      if (th === "dark") root.classList.add("dark");
      else if (th === "light") root.classList.remove("dark");
      else {
        const dark = window.matchMedia("(prefers-color-scheme: dark)").matches;
        if (dark) root.classList.add("dark"); else root.classList.remove("dark");
      }
    } catch { /* silent */ }
  };

  // G8 fix: use the single i18n setLocale to keep localStorage+cookie in sync.
  // Previously only cookie was written here, causing language to revert on reload.
  const handleSetLanguage = (code: string) => {
    setLanguage(code);
    try { setLocale(code as "en" | "zh"); } catch { /* silent */ }
    setTimeout(() => { window.location.reload(); }, 0);
  };

  const { mutate: vacuumDatabase, isPending: savingVacuum } = useMutation({
    fn: () => api.post(paths.settings.dbVacuum),
    onSuccess: () => toast.success(t("settings.toast.vacuum_done")),
    onError: (e) => toastError(e, "settings.toast.vacuum_failed"),
  });

  const handleVacuum = () => {
    void vacuumDatabase();
  };

  const { mutate: downloadBackup, isPending: savingBackup } = useMutation({
    fn: async () => {
      const { blob } = await api.download(paths.settings.dbBackup);
      downloadBlob(blob, `forgec2_backup_${Date.now()}.db`);
    },
    onSuccess: () => toast.success(t("settings.toast.backup_done")),
    onError: (e) => toastError(e, "settings.toast.backup_failed"),
  });

  const handleBackup = () => {
    void downloadBackup();
  };

  const handleDownloadDB = async () => {
    try {
      const { blob } = await api.downloadGet(paths.settings.configDownload);
      downloadBlob(blob, "forgec2.db");
    } catch (e) { toastError(e, "settings.toast.download_failed"); }
  };

  const { mutate: purgeData, isPending: savingPurge } = useMutation({
    fn: (type: string) => {
      const days = type === "tasks" ? purgeDays.tasks : purgeDays.audit;
      const path = type === "screenshots"
        ? paths.settings.maintenancePurge
        : paths.settings.purge(type);
      return api.post(path, { days });
    },
    onSuccess: (_result, type) => toast.success(t("settings.toast.purge_done", { type })),
    onError: (e, type) => toastError(e, "settings.toast.purge_failed", { type }),
  });

  const handlePurge = async (type: string) => {
    const days = type === "tasks" ? purgeDays.tasks : purgeDays.audit;
    if (!(await confirmAction({ message: t("settings.confirm.purge", { type, days }) }))) return;
    void purgeData(type);
  };

  const handleCheckUpdate = async () => {
    try {
      const d = await api.get<{
        update_available?: boolean; latest_version?: string; download_url?: string;
        check_ok?: boolean; check_error?: string;
      }>(paths.updateCheck);
      if (d.update_available && d.latest_version) {
        toast.success(t("settings.toast.update_new", { version: String(d.latest_version) }));
        openUpdateDialog({ latest: String(d.latest_version), downloadUrl: d.download_url });
      } else if (d.check_ok === false) {
        // The check itself failed: saying "you are up to date" would hide
        // the update that is waiting to be found.
        toast.error(d.check_error ? String(d.check_error) : t("settings.toast.update_failed"));
      } else {
        toast.success(t("settings.toast.update_latest"));
      }
    } catch (e) { toastError(e, "settings.toast.update_failed"); }
  };

  // One shared pending flag keeps every settings action button disabled while
  // any save is in flight (pre-migration behavior), now derived from the
  // per-action mutations instead of a hand-rolled setSaving.
  const saving =
    savingAgent || savingServer || savingMalleable || savingPassword || savingJwt ||
    savingVacuum || savingBackup || savingPurge;

  if (loading) return <PageContainer title={t("settings.title")} subtitle={t("settings.subtitle")}><PageSpinner /></PageContainer>;

  const settingsUnread = !settingsLoaded;
  const retrySettings = () => { void loadSettings(); };
  const gateSettingsData = (content: React.ReactNode) => (
    settingsUnread
      ? <DataError message={settingsError ?? t("settings.toast.load_failed")} onRetry={retrySettings} />
      : content
  );

  const sections = [
      { key: "appearance", label: t("settings.appearance"), icon: <Palette className="size-4" /> },
      { key: "security", label: t("settings.security"), icon: <Lock className="size-4" /> },
      { key: "server", label: t("settings.server"), icon: <Server className="size-4" /> },
    { key: "agent", label: t("settings.agent"), icon: <Bot className="size-4" /> },
    { key: "malleable", label: t("settings.malleable"), icon: <Shield className="size-4" /> },
    { key: "database", label: t("settings.database"), icon: <Database className="size-4" /> },
    { key: "backup", label: t("settings.backup"), icon: <Archive className="size-4" /> },
    { key: "maintenance", label: t("settings.maintenance"), icon: <Wrench className="size-4" /> },
    { key: "notifications", label: t("settings.notifications"), icon: <Bell className="size-4" /> },
    { key: "extc2", label: t("settings.extc2"), icon: <Radio className="size-4" /> },
    { key: "siem", label: t("settings.siem"), icon: <ScanSearch className="size-4" /> },
    { key: "certificates", label: t("settings.certificates.label"), icon: <Lock className="size-4" /> },
    { key: "modules", label: t("settings.modules.title"), icon: <FileCode className="size-4" /> },
      { key: "emergency", label: t("settings.emergency.title"), icon: <AlertTriangle className="size-4" /> },
      { key: "about", label: t("settings.about"), icon: <Cpu className="size-4" /> },
  ];

  const sectionGroups = [
      { key: "account", label: t("settings.group_account"), members: ["appearance", "security"] },
      { key: "server", label: t("settings.group_server"), members: ["server", "database", "backup", "certificates"] },
      { key: "agent", label: t("settings.group_agent"), members: ["agent", "malleable", "modules"] },
      { key: "integrations", label: t("settings.group_integrations"), members: ["notifications", "extc2", "siem"] },
    { key: "maintenance", label: t("settings.group_maintenance"), members: ["maintenance", "emergency", "about"] },
  ].map((group) => ({
    ...group,
    items: sections.filter((section) => group.members.includes(section.key)),
  }));
  const normalizedSectionQuery = sectionQuery.trim().toLocaleLowerCase();
  const visibleSectionGroups = sectionGroups
    .map((group) => ({
      ...group,
      items: normalizedSectionQuery
        ? group.items.filter((section) => section.label.toLocaleLowerCase().includes(normalizedSectionQuery))
        : group.items,
    }))
    .filter((group) => group.items.length > 0);
  const activeSectionMeta = sections.find((section) => section.key === activeSection) ?? sections[0];
  const activeGroup = sectionGroups.find((group) => group.members.includes(activeSection)) ?? sectionGroups[0];
  const activeSectionIndex = sections.findIndex((section) => section.key === activeSection);
  const handleSectionChange = (section: string) => {
    if (!SETTINGS_SECTION_KEYS.has(section)) return;
    setActiveSection(section);
    window.history.replaceState(null, "", `${window.location.pathname}${window.location.search}#tab=${encodeURIComponent(section)}`);
  };

  return (
    <Permission perms="settings.read" fallback={
      <PageContainer title={t("settings.title")} subtitle={t("settings.subtitle")}>
        <ErrorState title={t("common.denied_title")} message={t("common.denied_desc")} />
      </PageContainer>
    }>
    <PageContainer variant="standard" title={t("settings.title")} subtitle={t("settings.subtitle")}>

      {settingsLoaded && settingsError && (
        <div role="alert" className="mb-4 flex flex-wrap items-center gap-3 rounded-xl border border-warning/30 bg-warning/10 px-4 py-3 text-xs text-warning-ink">
          <AlertTriangle className="size-4 shrink-0" aria-hidden="true" />
          <span className="min-w-0 flex-1">{t("settings.refresh_failed_stale", { message: settingsError })}</span>
          <Button onClick={retrySettings} size="sm" variant="outline" className="min-h-11 px-4 sm:min-h-7 sm:px-2.5">
            {t("common.try_again")}
          </Button>
        </div>
      )}

      <Tabs value={activeSection} onValueChange={handleSectionChange} orientation="vertical">
        <div className="grid gap-4 lg:grid-cols-[18rem_minmax(0,1fr)] lg:items-start lg:gap-6 xl:grid-cols-[19rem_minmax(0,1fr)]">
          <aside className="hidden lg:block">
            <div className="sticky top-24 overflow-hidden rounded-xl border border-border/80 bg-card shadow-sm">
              <div className="border-b border-border/70 bg-muted/35 p-3">
                <div className="mb-2 flex items-center justify-between px-0.5">
                  <span className="text-xs font-semibold text-foreground">{t("settings.sidebar_header")}</span>
                  <span className="rounded-full bg-primary/10 px-2 py-0.5 font-mono text-[10px] font-semibold text-primary">{sections.length}</span>
                </div>
              <SearchInput
                value={sectionQuery}
                onChange={setSectionQuery}
                onClear={() => setSectionQuery("")}
                placeholder={t("settings.search_sections")}
                label={t("settings.search_sections")}
              />
              </div>
              <nav aria-label={t("settings.sidebar_header")} className="min-h-0 max-h-[calc(100vh-13rem)] overscroll-contain overflow-y-auto p-3 scrollbar-thin supports-[height:100dvh]:max-h-[calc(100dvh-13rem)]">
                <div className="space-y-5">
                  {visibleSectionGroups.map((group) => (
                    <div key={group.key}>
                      <div className="mono-eyebrow mb-1.5 px-2 text-muted-foreground">{group.label}</div>
                      <TabsList variant="sidebar" className="gap-1.5">
                        {group.items.map((s) => (
                          <TabsTrigger key={s.key} value={s.key}
                            className="relative flex min-h-11 w-full items-center justify-start gap-3 rounded-lg border-l-2 border-transparent px-3 py-2.5 text-sm text-muted-foreground transition-colors hover:bg-primary/5 data-[selected]:border-l-primary data-[selected]:bg-primary/10 data-[selected]:font-semibold data-[selected]:text-primary data-[selected]:shadow-none data-[selected]:[&>span:first-child]:bg-primary/10 data-[selected]:[&>span:first-child]:text-primary">
                            <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-muted/75 text-muted-foreground">{s.icon}</span>
                            <span className="truncate">{s.label}</span>
                          </TabsTrigger>
                        ))}
                      </TabsList>
                    </div>
                  ))}
                </div>
                {visibleSectionGroups.length === 0 && (
                  <div className="flex flex-col items-center px-3 py-8 text-center">
                    <SearchX className="mb-2 size-5 text-muted-foreground" />
                    <p className="text-xs text-muted-foreground">{t("settings.no_sections")}</p>
                    <button type="button" onClick={() => setSectionQuery("")} className="mt-2 text-xs font-medium text-primary hover:underline">{t("common.clear")}</button>
                  </div>
                )}
              </nav>
            </div>
          </aside>

          <div className="min-w-0">
            <div className="sticky top-0 z-20 mb-4 rounded-xl border border-border/80 bg-background/95 p-2 shadow-sm backdrop-blur lg:hidden">
              <Select value={activeSection} onValueChange={(value) => { if (typeof value === "string") handleSectionChange(value); }}>
                <SelectTrigger className="min-h-11 w-full bg-card" aria-label={t("settings.sidebar_header")}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {sectionGroups.map((group) => group.items.map((section) => (
                    <SelectItem key={section.key} value={section.key}>{group.label} · {section.label}</SelectItem>
                  )))}
                </SelectContent>
              </Select>
            </div>

            <div className="mb-4 flex items-center gap-3 rounded-xl border border-border/75 bg-card px-4 py-3 shadow-xs">
              <span className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary">{activeSectionMeta.icon}</span>
              <div className="min-w-0 flex-1">
                <div className="mono-eyebrow text-muted-foreground">{activeGroup.label}</div>
                <h2 className="truncate text-base font-semibold text-foreground">{activeSectionMeta.label}</h2>
              </div>
              <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{activeSectionIndex + 1}/{sections.length}</span>
            </div>

            <div className="flex flex-col gap-(--page-gap)">
        <TabsContent value="appearance" className="mt-0"><AppearanceSection theme={theme} onApplyTheme={handleApplyTheme} language={language} onSetLanguage={handleSetLanguage} /></TabsContent>
              <TabsContent value="security" className="mt-0">
                <SecuritySection
                  data={data} passwordForm={passwordForm} setPasswordForm={setPasswordForm}
                  saving={saving} onChangePassword={handleChangePassword} onRegenerateJWT={handleRegenerateJWT}
                />
                <div className="mt-4"><ApiKeysSection /></div>
              </TabsContent>
              <TabsContent value="server" className="mt-0">
                <ServerSection data={data} form={serverForm} setForm={setServerForm} saving={saving} onSave={handleSaveServer} settingsLoaded={settingsLoaded} />
                <div className="mt-4"><Suspense fallback={null}><ReloadStatusCard /></Suspense></div>
              </TabsContent>
              <TabsContent value="agent" className="mt-0"><AgentSection form={agentForm} setForm={setAgentForm} saving={saving} onSave={handleSaveAgent} settingsLoaded={settingsLoaded} /></TabsContent>
              <TabsContent value="malleable" className="mt-0"><Suspense fallback={null}><MalleableSection form={malleableForm} setForm={setMalleableForm} saving={saving} onSave={handleSaveMalleable} settingsLoaded={settingsLoaded} /></Suspense></TabsContent>
              <TabsContent value="database" className="mt-0"><Suspense fallback={null}>{gateSettingsData(<DatabaseSection data={data} saving={saving} onVacuum={handleVacuum} onBackup={handleBackup} onDownloadDB={handleDownloadDB} />)}</Suspense></TabsContent>
              <TabsContent value="backup" className="mt-0"><Suspense fallback={null}><BackupSection /></Suspense></TabsContent>
              <TabsContent value="maintenance" className="mt-0"><Suspense fallback={null}><MaintenanceSection purgeDays={purgeDays} setPurgeDays={setPurgeDays} saving={saving} onPurge={handlePurge} onPurgeScreenshots={async () => { if (await confirmPurge({ message: t("settings.confirm.purge_screenshots"), confirmText: t("settings.btn.purge") })) handlePurge("screenshots"); }} /></Suspense></TabsContent>
              <TabsContent value="notifications" className="mt-0"><Suspense fallback={null}><NotificationsSection /></Suspense></TabsContent>
              <TabsContent value="about" className="mt-0"><Suspense fallback={null}>{gateSettingsData(<AboutSection data={data} onCheckUpdate={handleCheckUpdate} />)}</Suspense></TabsContent>
              <TabsContent value="extc2" className="mt-0"><Suspense fallback={null}><ExtC2Section /></Suspense></TabsContent>
              <TabsContent value="siem" className="mt-0"><Suspense fallback={null}><SIEMRulesSection /></Suspense></TabsContent>
              <TabsContent value="certificates" className="mt-0"><Suspense fallback={null}>{gateSettingsData(<CertificatesSection data={data} saving={saving} onRefresh={loadSettings} />)}</Suspense></TabsContent>
              <TabsContent value="modules" className="mt-0"><Suspense fallback={null}><ModulesSection /></Suspense></TabsContent>
              <TabsContent value="emergency" className="mt-0"><Suspense fallback={null}><EmergencySection /></Suspense></TabsContent>
            </div>
          </div>
        </div>
      </Tabs>

      {modalAction}
      {modalPurge}
    </PageContainer>
    </Permission>
  );
}
