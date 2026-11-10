
import { registerFragment } from "@/lib/i18n";
import { enPhishing } from "@/lib/i18n/fragments/en.phishing";
registerFragment(enPhishing);

import { useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { EmptyState } from "@/components/ui/empty-state";
import { PageContainer } from "@/components/ui/page-container";
import { PageSpinner } from "@/components/ui/spinner";
import { DataError } from "@/components/ui/data-state";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { useApiResource } from "@/lib/hooks/useApiResource";
import { useErrorToast } from "@/lib/hooks/useErrorToast";
import { useMutation } from "@/lib/hooks/useMutation";
import { formatTime } from "@/lib/utils";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";
import { FileText, Key, Pencil, Play, Plus, Save, Send, Square, Trash2, Shield } from "lucide-react";
import { EntraIdentityPanel } from "./components/EntraIdentityPanel";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

interface PhishingTemplate {
  id: number;
  ID: number;
  name: string;
  subject: string;
  body: string;
  from_name: string;
  from_email: string;
  type: string;
  created_by: string;
  created_at: string;
}

interface PhishingCampaign {
  id: number;
  ID: number;
  name: string;
  template_id: number;
  target_list: string;
  smtp_host: string;
  smtp_port: number;
  smtp_user: string;
  smtp_pass: string;
  status: string;
  sent_count: number;
  failed_count: number;
  open_count: number;
  cred_count: number;
  created_by: string;
  created_at: string;
}

interface CaptureEntry {
  id: number;
  ID: number;
  username: string;
  password: string;
  domain: string;
  source: string;
  type: string;
  created_at: string;
}

type Tab = "templates" | "campaigns" | "captures" | "entra";

const EMPTY_TPL_FORM = { name: "", subject: "", body: "", from_name: "", from_email: "", type: "html" };
const EMPTY_CAMP_FORM = { name: "", template_id: 0, target_list: "", smtp_host: "", smtp_port: 587, smtp_user: "", smtp_pass: "" };

export default function PhishingPageContent() {
  const { t } = useI18n();
  const { confirm, modal } = useConfirm();
  const { toastError } = useErrorToast();

  // Template form
  const [showTplForm, setShowTplForm] = useState(false);
  const [tplForm, setTplForm] = useState(EMPTY_TPL_FORM);
  const [editTplId, setEditTplId] = useState<number | null>(null);

  // Campaign form
  const [showCampForm, setShowCampForm] = useState(false);
  const [campForm, setCampForm] = useState(EMPTY_CAMP_FORM);

  const tabs: { key: Tab; label: string; icon: React.ReactNode }[] = [
    { key: "templates", label: t("phishing.tab_templates"), icon: <FileText className="size-4" /> },
    { key: "campaigns", label: t("phishing.tab_campaigns"), icon: <Send className="size-4" /> },
    { key: "captures", label: t("phishing.tab_captures"), icon: <Key className="size-4" /> },
    { key: "entra", label: t("phishing.tab_entra"), icon: <Shield className="size-4" /> },
  ];

  const { data, loading, error: loadError, refresh: loadAll } = useApiResource<{
    templates: PhishingTemplate[];
    campaigns: PhishingCampaign[];
    captures: CaptureEntry[];
  }>({
    fetcher: async () => {
      const [t, c, cap] = await Promise.all([
        api.get(paths.phishing.templates),
        api.get(paths.phishing.campaigns),
        api.get(paths.phishing.captures),
      ]);
      return {
        templates: (t.data as PhishingTemplate[]) || [],
        campaigns: (c.data as PhishingCampaign[]) || [],
        captures: (cap.data as CaptureEntry[]) || [],
      };
    },
    errorMessage: t("phishing.toast.load_failed"),
  });
  const templates = data?.templates ?? [];
  const campaigns = data?.campaigns ?? [];
  const captures = data?.captures ?? [];

  // 鈹€鈹€ Template CRUD 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

  const { mutate: saveTemplate, isPending: savingTpl } = useMutation({
    fn: () =>
      editTplId
        ? api.putJson(paths.phishing.template(editTplId), tplForm)
        : api.postJson(paths.phishing.templates, tplForm),
    onSuccess: () => {
      setShowTplForm(false);
      setEditTplId(null);
      setTplForm(EMPTY_TPL_FORM);
      loadAll();
    },
    onError: (e) => toastError(e, "phishing.toast.save_template_failed"),
  });

  const handleSaveTpl = () => {
    if (savingTpl) return;
    void saveTemplate();
  };

  const handleEditTpl = (t: PhishingTemplate) => {
    setEditTplId(t.id);
    setTplForm({ name: t.name, subject: t.subject, body: t.body, from_name: t.from_name, from_email: t.from_email, type: t.type || "html" });
    setShowTplForm(true);
  };

  const { mutate: deleteTemplate } = useMutation({
    fn: (id: number) => api.del(paths.phishing.template(id)),
    onSuccess: () => loadAll(),
    onError: (e) => toastError(e, "phishing.toast.delete_template_failed"),
  });

  const handleDeleteTpl = async (id: number) => {
    if (!(await confirm({ message: t("phishing.delete_template") }))) return;
    void deleteTemplate(id);
  };

  // 鈹€鈹€ Campaign CRUD 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

  const { mutate: saveCampaign, isPending: savingCamp } = useMutation({
    fn: () => api.postJson(paths.phishing.campaigns, campForm),
    onSuccess: () => {
      setShowCampForm(false);
      setCampForm(EMPTY_CAMP_FORM);
      loadAll();
    },
    onError: (e) => toastError(e, "phishing.toast.save_campaign_failed"),
  });

  const handleSaveCamp = () => {
    if (!campForm.template_id) {
      toast.error(t("phishing.toast.select_template")); // validation, not an API failure
      return;
    }
    if (savingCamp) return;
    void saveCampaign();
  };

  // The launch spinner names which campaign is in flight; the mutation owns
  // error routing and the stale-completion guard.
  const [launchingId, setLaunchingId] = useState<number | null>(null);
  const { mutate: launchCampaign } = useMutation({
    fn: (id: number) =>
      api.postJson<{ success?: boolean; queued?: number; message?: string; error?: string }>(
        `/phishing/campaigns/${id}/launch`,
        {},
      ),
    onSuccess: (res) => {
      if (res.message) toast.success(res.message);
      else toast.success(t("phishing.toast.launched", { count: res.queued ?? 0 }));
      loadAll();
    },
    onError: (e) => toastError(e, "phishing.toast.launch_failed"),
  });

  const handleLaunch = async (id: number) => {
    // Double-click guard: two launches could email the full target list
    // twice (the backend claim is atomic, but the second request still
    // errors confusingly).
    if (launchingId !== null) return;
    setLaunchingId(id);
    try {
      await launchCampaign(id);
    } finally {
      setLaunchingId(null);
    }
  };

  const { mutate: stopCampaign } = useMutation({
    fn: (id: number) => api.post(paths.phishing.campaignStop(id)),
    onSuccess: () => loadAll(),
    onError: (e) => toastError(e, "phishing.toast.stop_failed"),
  });

  const { mutate: deleteCampaign } = useMutation({
    fn: (id: number) => api.del(paths.phishing.campaign(id)),
    onSuccess: () => loadAll(),
    onError: (e) => toastError(e, "phishing.toast.delete_campaign_failed"),
  });

  const handleDeleteCamp = async (id: number) => {
    if (!(await confirm({ message: t("phishing.delete_campaign") }))) return;
    void deleteCampaign(id);
  };

  if (loading) return <PageContainer title={t("phishing.title")} subtitle={t("phishing.subtitle")}><PageSpinner /></PageContainer>;

  // The inline error + retry is the load-failure surface (the config/traffic
  // page loads do not toast either); an empty-state render would read as
  // "nothing configured" instead of "could not load".
  if (loadError) {
    return (
      <PageContainer title={t("phishing.title")} subtitle={t("phishing.subtitle")}>
        <DataError message={loadError} onRetry={() => { void loadAll(); }} />
      </PageContainer>
    );
  }

  // 鈹€鈹€ Templates Tab 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

  function renderTemplates() {
    return (
      <div>
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg font-semibold text-foreground">{t("phishing.templates_title")}</h2>
          <Button onClick={() => { setEditTplId(null); setTplForm(EMPTY_TPL_FORM); setShowTplForm(true); }}>
            <Plus className="size-4" /> {t("phishing.new_template")}
          </Button>
        </div>
        {templates.length === 0 ? (
          <EmptyState icon={FileText} title={t("phishing.empty_templates")} message={t("phishing.empty_templates_hint")} />
        ) : (
          <Card>
            <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="text-left px-4 py-3 sm:py-3.5">{t("phishing.col_name")}</TableHead>
                  <TableHead className="text-left px-4 py-3 sm:py-3.5">{t("phishing.col_subject")}</TableHead>
                  <TableHead className="text-left px-4 py-3 sm:py-3.5">{t("phishing.col_from")}</TableHead>
                  <TableHead className="text-left px-4 py-3 sm:py-3.5">{t("phishing.col_type")}</TableHead>
                  <TableHead className="text-left px-4 py-3 sm:py-3.5">{t("groups.created_by")}</TableHead>
                  <TableHead className="text-right px-4 py-3 sm:py-3.5">{t("phishing.col_actions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {templates.map((tpl) => {
                  const tid = tpl.id;
                  return (
                    <TableRow key={tid}>
                      <TableCell className="px-4 py-3 sm:py-3.5 font-medium text-foreground">{tpl.name}</TableCell>
                      <TableCell className="px-4 py-3 sm:py-3.5 text-muted-foreground max-w-[200px] truncate">{tpl.subject}</TableCell>
                      <TableCell className="px-4 py-3 sm:py-3.5 text-muted-foreground">{tpl.from_name || tpl.from_email}</TableCell>
                      <TableCell className="px-4 py-3 sm:py-3.5"><Badge variant="outline">{tpl.type}</Badge></TableCell>
                      <TableCell className="px-4 py-3 sm:py-3.5 text-muted-foreground text-xs">{tpl.created_by || "-"}</TableCell>
                      <TableCell className="px-4 py-3 sm:py-3.5 text-right">
                        <Button variant="ghost" size="icon-xs" onClick={() => handleEditTpl(tpl)} className="text-muted-foreground hover:text-primary mr-3" aria-label={t("phishing.a11y_edit_template")}><Pencil className="size-4" /></Button>
                        <Button variant="ghost" size="icon-xs" onClick={() => handleDeleteTpl(tid)} className="text-muted-foreground hover:text-destructive-ink" aria-label={t("phishing.a11y_delete_template")}><Trash2 className="size-4" /></Button>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
            </div>
          </Card>
        )}
        <Dialog open={showTplForm} onOpenChange={(open) => {
          setShowTplForm(open);
          if (!open) setEditTplId(null);
        }}>
          <DialogContent size="2xl" className="max-h-[90vh] overflow-y-auto" showCloseButton={false}>
            <DialogHeader>
              <DialogTitle>{editTplId ? t("phishing.dialog_edit_template") : t("phishing.dialog_new_template")}</DialogTitle>
            </DialogHeader>
            <div className="space-y-4">
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                <div>
                  <Label htmlFor="phishing-field-template-name">{t("phishing.field_template_name")}</Label>
                  <Input id="phishing-field-template-name" value={tplForm.name} onChange={(e) => setTplForm({ ...tplForm, name: e.target.value })} />
                </div>
                <div>
                  <Label htmlFor="common-type">{t("common.type")}</Label>
                  <Select id="common-type" value={tplForm.type} onValueChange={(v) => setTplForm({ ...tplForm, type: v || "html" })}>
                    <SelectTrigger className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="html">{t("phishing.type_html")}</SelectItem>
                      <SelectItem value="text">{t("phishing.type_plain")}</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              </div>
              <div>
                <Label htmlFor="phishing-field-subject">{t("phishing.field_subject")}</Label>
                <Input id="phishing-field-subject" value={tplForm.subject} onChange={(e) => setTplForm({ ...tplForm, subject: e.target.value })} />
              </div>
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                <div>
                  <Label htmlFor="phishing-field-from-name">{t("phishing.field_from_name")}</Label>
                  <Input id="phishing-field-from-name" value={tplForm.from_name} onChange={(e) => setTplForm({ ...tplForm, from_name: e.target.value })} />
                </div>
                <div>
                  <Label htmlFor="phishing-field-from-email">{t("phishing.field_from_email")}</Label>
                  <Input id="phishing-field-from-email" value={tplForm.from_email} onChange={(e) => setTplForm({ ...tplForm, from_email: e.target.value })} />
                </div>
              </div>
              <div>
                <Label>
                  {t("phishing.field_body")}
                  <span className="text-xs text-muted-foreground ml-2">Supports {'{{.Link}}'}, {'{{.Token}}'}, {'{{.Email}}'}, {'{{.Username}}'}</span>
                </Label>
                <Textarea rows={12} className="font-mono" value={tplForm.body} onChange={(e) => setTplForm({ ...tplForm, body: e.target.value })} />
              </div>
            </div>
            <div className="flex gap-3">
              <Button onClick={handleSaveTpl} disabled={savingTpl} className="flex-1"><Save className="size-4" />{t("common.save")}</Button>
              <Button variant="outline" onClick={() => { setShowTplForm(false); setEditTplId(null); }}>{t("common.cancel")}</Button>
            </div>
          </DialogContent>
        </Dialog>
      </div>
    );
  }

  // 鈹€鈹€ Campaigns Tab 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

  function renderCampaigns() {
    const statusBadge = (s: string) => {
      const label = s === "stopped" ? t("phishing.stopped") : s;
      const variant = s === "running" ? "warning" as const : s === "completed" ? "success" as const : s === "stopped" ? "destructive" as const : "secondary" as const;
      return <Badge variant={variant}>{label}</Badge>;
    };
    return (
      <div>
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg font-semibold text-foreground">{t("phishing.campaigns_title")}</h2>
          <Button onClick={() => setShowCampForm(true)}>
            <Plus className="size-4" /> {t("phishing.new_campaign")}
          </Button>
        </div>
        {campaigns.length === 0 ? (
          <EmptyState icon={Send} title={t("campaign.empty")} />
        ) : (
          <Card>
            <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="text-left px-4 py-3 sm:py-3.5">{t("phishing.col_name")}</TableHead>
                  <TableHead className="text-left px-4 py-3 sm:py-3.5">{t("phishing.col_status")}</TableHead>
                  <TableHead className="text-right px-4 py-3 sm:py-3.5">{t("common.total")}</TableHead>
                  <TableHead className="text-right px-4 py-3 sm:py-3.5">{t("phishing.col_from")}</TableHead>
                  <TableHead className="text-right px-4 py-3 sm:py-3.5">{t("phishing.col_subject")}</TableHead>
                  <TableHead className="text-right px-4 py-3 sm:py-3.5">{t("groups.created_by")}</TableHead>
                  <TableHead className="text-right px-4 py-3 sm:py-3.5">{t("phishing.col_actions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {campaigns.map((c) => {
                  const cid = c.id;
                  return (
                    <TableRow key={cid}>
                      <TableCell className="px-4 py-3 sm:py-3.5 font-medium text-foreground">{c.name}</TableCell>
                      <TableCell className="px-4 py-3 sm:py-3.5">{statusBadge(c.status)}</TableCell>
                      <TableCell className="px-4 py-3 sm:py-3.5 text-right text-muted-foreground">{c.sent_count}{c.failed_count > 0 && <span className="text-destructive-ink"> ({t("phishing.sends_failed", { n: c.failed_count })})</span>}</TableCell>
                      <TableCell className="px-4 py-3 sm:py-3.5 text-right text-muted-foreground">{c.open_count}</TableCell>
                      <TableCell className="px-4 py-3 sm:py-3.5 text-right text-muted-foreground">{c.cred_count}</TableCell>
                      <TableCell className="px-4 py-3 sm:py-3.5 text-right text-muted-foreground text-xs">{c.created_by || "-"}</TableCell>
                      <TableCell className="px-4 py-3 sm:py-3.5 text-right">
                        {c.status === "draft" && <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon-xs" disabled={launchingId === cid} onClick={() => handleLaunch(cid)} className="text-primary hover:text-primary dark:hover:text-primary mr-3" aria-label={t("phishing.launch")} />}><Play className="size-4" /></TooltipTrigger><TooltipContent>{t("phishing.launch")}</TooltipContent></Tooltip>}
                        {c.status === "running" && <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon-xs" onClick={() => { void stopCampaign(cid); }} className="text-warning-ink hover:text-warning-ink/80 mr-3" aria-label={t("phishing.stop")} />}><Square className="size-4" /></TooltipTrigger><TooltipContent>{t("phishing.stop")}</TooltipContent></Tooltip>}
                        {(c.status === "draft" || c.status === "completed" || c.status === "stopped") && <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon-xs" onClick={() => handleDeleteCamp(cid)} className="text-muted-foreground hover:text-destructive-ink" aria-label={t("phishing.delete")} />}><Trash2 className="size-4" /></TooltipTrigger><TooltipContent>{t("phishing.delete")}</TooltipContent></Tooltip>}
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
            </div>
          </Card>
        )}
        <Dialog open={showCampForm} onOpenChange={setShowCampForm}>
          <DialogContent size="2xl" className="max-h-[90vh] overflow-y-auto" showCloseButton={false}>
            <DialogHeader>
              <DialogTitle>{t("phishing.new_campaign")}</DialogTitle>
            </DialogHeader>
            <div className="space-y-4">
              <div>
                <Label htmlFor="phishing-campaign-name">{t("phishing.campaign_name")}</Label>
                <Input id="phishing-campaign-name" value={campForm.name} onChange={(e) => setCampForm({ ...campForm, name: e.target.value })} />
              </div>
              <div>
                <Label htmlFor="phishing-field-template">{t("phishing.field_template")}</Label>
                <Select id="phishing-field-template" value={String(campForm.template_id)} onValueChange={(v) => {
                  if (v !== null) setCampForm({ ...campForm, template_id: parseInt(v) });
                }}>
                  <SelectTrigger className="w-full">
                    <SelectValue placeholder={t("phishing.field_template")} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="0">{t("phishing.field_template")}</SelectItem>
                    {templates.map((t) => (<SelectItem key={t.id} value={String(t.id)}>{t.name}</SelectItem>))}
                  </SelectContent>
                </Select>
              </div>
              <div>
                <Label htmlFor="phishing-field-targets">{t("phishing.field_targets")}</Label>
                <Textarea id="phishing-field-targets" rows={4} className="font-mono" value={campForm.target_list} onChange={(e) => setCampForm({ ...campForm, target_list: e.target.value })} placeholder='["user1@example.com","user2@example.com"]' />
              </div>
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                <div>
                  <Label htmlFor="phishing-field-smtp-host">{t("phishing.field_smtp_host")}</Label>
                  <Input id="phishing-field-smtp-host" value={campForm.smtp_host} onChange={(e) => setCampForm({ ...campForm, smtp_host: e.target.value })} />
                </div>
                <div>
                  <Label htmlFor="phishing-field-smtp-port">{t("phishing.field_smtp_port")}</Label>
                  <Input id="phishing-field-smtp-port" type="number" value={campForm.smtp_port} onChange={(e) => setCampForm({ ...campForm, smtp_port: parseInt(e.target.value, 10) || 587 })} />
                </div>
              </div>
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                <div>
                  <Label htmlFor="phishing-field-smtp-user">{t("phishing.field_smtp_user")}</Label>
                  <Input id="phishing-field-smtp-user" value={campForm.smtp_user} onChange={(e) => setCampForm({ ...campForm, smtp_user: e.target.value })} />
                </div>
                <div>
                  <Label htmlFor="phishing-field-smtp-pass">{t("phishing.field_smtp_pass")}</Label>
                  <Input id="phishing-field-smtp-pass" type="password" value={campForm.smtp_pass} onChange={(e) => setCampForm({ ...campForm, smtp_pass: e.target.value })} />
                </div>
              </div>
            </div>
            <div className="flex gap-3">
              <Button onClick={handleSaveCamp} disabled={savingCamp} className="flex-1"><Save className="size-4" />{t("phishing.create_campaign")}</Button>
              <Button variant="outline" onClick={() => setShowCampForm(false)}>{t("common.cancel")}</Button>
            </div>
          </DialogContent>
        </Dialog>
      </div>
    );
  }

  // 鈹€鈹€ Captures Tab 鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€鈹€

  function renderCaptures() {
    const typeBadge = (t: string) => {
      const variant = t === "cleartext" ? "success" as const : t === "ntlm" ? "warning" as const : "secondary" as const;
      return <Badge variant={variant}>{t}</Badge>;
    };
    return (
      <div>
        <h2 className="text-lg font-semibold text-foreground mb-4">{t("phishing.captures_title")}</h2>
        {captures.length === 0 ? (
          <EmptyState icon={Key} title={t("phishing.empty_captures")} message={t("phishing.empty_captures_hint")} />
        ) : (
          <Card>
            <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="text-left px-4 py-3 sm:py-3.5">{t("users.label_username")}</TableHead>
                  <TableHead className="text-left px-4 py-3 sm:py-3.5">{t("phishing.col_password")}</TableHead>
                  <TableHead className="text-left px-4 py-3 sm:py-3.5">{t("phishing.col_type")}</TableHead>
                  <TableHead className="text-left px-4 py-3 sm:py-3.5">{t("phishing.col_from")}</TableHead>
                  <TableHead className="text-left px-4 py-3 sm:py-3.5">{t("phishing.col_date")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {captures.map((cr) => {
                  const cid = cr.id;
                  return (
                    <TableRow key={cid}>
                      <TableCell className="px-4 py-3 sm:py-3.5 font-medium text-foreground">{cr.username}</TableCell>
                      <TableCell className="px-4 py-3 sm:py-3.5 text-muted-foreground font-mono text-xs">{cr.password ? cr.password.substring(0, 40) + (cr.password.length > 40 ? "..." : "") : "-"}</TableCell>
                      <TableCell className="px-4 py-3 sm:py-3.5">{typeBadge(cr.type)}</TableCell>
                      <TableCell className="px-4 py-3 sm:py-3.5 text-muted-foreground text-xs">{cr.source}</TableCell>
                      <TableCell className="px-4 py-3 sm:py-3.5 text-muted-foreground text-xs">{cr.created_at ? formatTime(cr.created_at) : ""}</TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
            </div>
          </Card>
        )}
      </div>
    );
  }

  return (
    <PageContainer title={t("phishing.title")} subtitle={t("phishing.subtitle")}>

      {/* Tabs */}
      <Tabs defaultValue="templates">
        <TabsList sticky>
          {tabs.map((tab) => (
            <TabsTrigger key={tab.key} value={tab.key} className="gap-1.5">
              {tab.icon}
              {tab.label}
            </TabsTrigger>
          ))}
        </TabsList>

      <TabsContent value="templates">
        {renderTemplates()}
      </TabsContent>

      <TabsContent value="campaigns">
        {renderCampaigns()}
      </TabsContent>

      <TabsContent value="captures">
        {renderCaptures()}
      </TabsContent>

      <TabsContent value="entra">
        <EntraIdentityPanel />
      </TabsContent>
      </Tabs>

      {modal}
    </PageContainer>
  );
}

