
import { registerFragment } from "@/lib/i18n";
import { enInfra } from "@/lib/i18n/fragments/en.infra";
registerFragment(enInfra);

import { useEffect } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";
import { errorText } from "@/lib/errors";
import { useMutation } from "@/lib/hooks/useMutation";
import { useErrorToast } from "@/lib/hooks/useErrorToast";
import { Banner } from "@/components/ui/banner";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { PageContainer } from "@/components/ui/page-container";
import { Spinner } from "@/components/ui/spinner";
import { Card, CardContent } from "@/components/ui/card";
import { CardHeader } from "@/components/ui/card-header";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Badge } from "@/components/ui/badge";
import { StatusDot } from "@/components/ui/status-dot";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Checkbox } from "@/components/ui/checkbox";
import { Award, CheckCircle, CircleAlert, Code, Copy, Download, FileUp, FileDown, Globe, Info, Mail, Pencil, Plug, Plus, Rocket, Save, Server, Share2, ShieldCheck, Terminal, Trash2, WandSparkles } from "lucide-react";
import { DataState } from "@/components/ui/data-state";
import { PageSkeleton } from "@/components/ui/page-skeleton";
import { useInfrastructureData } from "./components/useInfrastructureData";
import { useInfrastructureDialogs } from "./components/useInfrastructureDialogs";
import { useInfrastructureConfigForm } from "./components/useInfrastructureConfigForm";
import { useInfrastructureRedirectorForm } from "./components/useInfrastructureRedirectorForm";

export default function InfrastructurePage() {
  const { t } = useI18n();
  const { listeners, redirectors, loading, error, loadListeners, loadRedirectors } = useInfrastructureData();
  const { showGenModal, setShowGenModal, confirm, modal, editingRd, setEditingRd } = useInfrastructureDialogs();
  const {
    selectedListener, setSelectedListener, domain, setDomain, port, setPort,
    certPath, setCertPath, keyPath, setKeyPath, wsSupport, setWsSupport,
    extC2Path, setExtC2Path, configOutput, configType,
    generating, copied, activeSection, setActiveSection,
    acmeDomain, setAcmeDomain, acmeEmail, setAcmeEmail, acmePort, setAcmePort,
    acmeStaging, setAcmeStaging, acmeProvisioning, exportFormat, setExportFormat,
    exporting, generateConfig, copyConfig, downloadConfig, provisionCert, exportProfile,
  } = useInfrastructureConfigForm(listeners);
  const {
    rdName, setRdName, rdHost, setRdHost, rdType, setRdType,
    rdSSHUser, setRdSSHUser, rdSSHPort, setRdSSHPort,
    rdSSHKey, setRdSSHKey, rdSSHPassword, setRdSSHPassword,
    rdConfig, setRdConfig, rdGenerateHost, setRdGenerateHost,
    rdGenerateDomain, setRdGenerateDomain, rdGeneratePort, setRdGeneratePort,
    rdGenerateTLS, setRdGenerateTLS, rdGenerateWS, setRdGenerateWS,
    deleting, setDeleting,
    deployResult, setDeployResult, backendURL, setBackendURL,
  } = useInfrastructureRedirectorForm();

  const { toastError } = useErrorToast();

  const saveRedirector = useMutation({
    fn: () => {
      const payload = editingRd
        ? { name: rdName, host: rdHost, type: rdType, ssh_user: rdSSHUser, ssh_port: rdSSHPort, ssh_key: rdSSHKey, ssh_password: rdSSHPassword, config: rdConfig }
        : { name: rdName, host: rdHost, type: rdType, ssh_user: rdSSHUser, ssh_port: rdSSHPort, ssh_key: rdSSHKey, ssh_password: rdSSHPassword };
      return editingRd ? api.putJson(paths.redirectors.one(editingRd), payload) : api.postJson(paths.redirectors.list, payload);
    },
    onSuccess: () => { loadRedirectors(); setEditingRd(null); },
    onError: (e) => toastError(e, "infrastructure.toast.save_redirector_failed"),
  });

  const removeRedirector = useMutation({
    // The row-scoped spinner (`deleting === rd.id`) keeps its identity in
    // local state; the mutation tracks pending-ness only.
    fn: (id: number) => api.del(paths.redirectors.one(id)),
    onSuccess: () => loadRedirectors(),
    onError: (e) => toastError(e, "infrastructure.toast.delete_redirector_failed"),
  });

  const testSSH = useMutation({
    fn: () => api.postJson<{ success?: boolean; message?: string }>(paths.redirectors.testSsh, { host: rdHost, port: rdSSHPort, user: rdSSHUser, password: rdSSHPassword, ssh_key: rdSSHKey }),
    // The backend reports reachability in `message`; the old call read a
    // nonexistent `stdout` field and rendered "SSH OK: undefined".
    onSuccess: (data) => toast.success(t("infrastructure.toast.ssh_ok", { message: String(data.message ?? "") })),
    onError: (e) => toastError(e, "infrastructure.toast.ssh_test_failed"),
  });

  const generateRedirector = useMutation({
    fn: async () => {
      const data = await api.postJson<{ config?: string }>(`/redirectors/generate/${rdType}`, {
        domain: rdGenerateDomain,
        listen_port: rdGeneratePort,
        backend_url: backendURL,
        ws_enabled: rdGenerateWS,
      });
      if (!data.config) return;
      setRdConfig(data.config);
      const payload = {
        name: rdName || rdGenerateHost,
        host: rdGenerateHost,
        type: rdType,
        ssh_user: rdSSHUser,
        ssh_port: rdSSHPort,
        ssh_key: rdSSHKey,
        ssh_password: rdSSHPassword,
        config: data.config,
      };
      if (editingRd) {
        await api.putJson(paths.redirectors.one(editingRd), payload);
      } else {
        await api.postJson(paths.redirectors.list, payload);
      }
    },
    onError: (e) => toastError(e, "infrastructure.toast.generate_config_failed"),
  });

  const deploySSH = useMutation({
    fn: () => api.postJson<{ success: boolean; message?: string; stdout?: string; stderr?: string }>("/redirectors/deploy-ssh", {
      host: rdGenerateHost, port: rdSSHPort, user: rdSSHUser, password: rdSSHPassword,
      ssh_key: rdSSHKey, config: rdConfig, config_type: rdType,
    }),
    onSuccess: (data) => { setDeployResult(data); loadRedirectors(); },
    // The banner is the failure surface; a toast would only repeat it.
    onError: (e) => setDeployResult({ success: false, message: errorText(t, e, () => t("infra.unknown_error")) }),
  });

  useEffect(() => { loadListeners(); }, [loadListeners]);

  useEffect(() => {
    if (activeSection === "redirectors") {
      loadRedirectors();
    }
  }, [activeSection, loadRedirectors]);

  const currentListener = listeners.find(l => l.id === selectedListener);

  const sections = [
    { key: "config" as const, label: t("infra.reverse_proxy_config"), icon: <Code className="size-4" /> },
    { key: "redirectors" as const, label: t("infra.redirectors"), icon: <Share2 className="size-4" /> },
    { key: "acme" as const, label: t("infra.acme_cert"), icon: <ShieldCheck className="size-4" /> },
    { key: "export" as const, label: t("infra.c2_profile_tab"), icon: <FileDown className="size-4" /> },
  ];

  return (
    <PageContainer title={t("infra.title")} icon={<Server className="size-4" />} subtitle={t("infra.subtitle")}>

      <DataState loading={loading} error={error} onRetry={() => { loadListeners(); if (activeSection === "redirectors") loadRedirectors(); }} loadingSkeleton={<PageSkeleton />}>
      <Tabs value={activeSection} onValueChange={setActiveSection}>
        <TabsList className="flex w-full justify-start rounded-none border-b border-border bg-transparent p-0 h-auto">
          {sections.map(s => (
            <TabsTrigger key={s.key} value={s.key}
              className="flex items-center gap-x-2 px-5 py-3 text-sm font-medium rounded-none border-b-2 border-transparent data-[selected]:border-primary data-[selected]:text-primary text-muted-foreground hover:text-foreground transition-colors">
              {s.icon}
              <span>{s.label}</span>
            </TabsTrigger>
          ))}
        </TabsList>

        <TabsContent value="config" className="mt-0">
          <Card className="gap-0 overflow-hidden py-0 shadow-sm hover:shadow-md transition-shadow duration-200">
            <CardHeader
              icon={Plug}
              tone="primary"
              title={<>{t("infra.select")} Listener</>}
              description={t("infra.select_listener_hint")}
            />
            <CardContent className="p-(--card-spacing)">
              <Select value={selectedListener} onValueChange={(v) => setSelectedListener(v ?? "")}>
                <SelectTrigger className="max-w-md">
                  <SelectValue placeholder={t("infra.select_listener_placeholder")} />
                </SelectTrigger>
                <SelectContent>
                  {listeners.map(l => (
                    <SelectItem key={l.id} value={l.id}>{l.name} ({l.protocol}://{l.host}:{l.port})</SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {currentListener && (
                <div className="mt-3 text-xs text-muted-foreground">
                  <Info className="size-4" />
                  {t("infra.forwarding_to")} {currentListener.protocol}://{currentListener.host}:{currentListener.port}
                </div>
              )}
            </CardContent>
          </Card>

          <Card className="gap-0 overflow-hidden py-0">
            <CardHeader
              icon={Globe}
              tone="success"
              title={t("infra.domain_params")}
              description={t("infra.domain_desc")}
            />
            <CardContent className="p-(--card-spacing) space-y-4">
              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div>
                  <Label htmlFor="infra-domain">{t("infra.domain")}</Label>
                  <Input id="infra-domain" name="input-1" type="text" value={domain} onChange={e => setDomain(e.target.value)} placeholder="c2.example.com" />
                </div>
                <div>
                  <Label htmlFor="infra-listen-port">{t("infra.listen_port")}</Label>
                  <Input id="infra-listen-port" name="input-2" type="number" value={port} onChange={e => setPort(Number(e.target.value))} />
                </div>
              </div>
              <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                <div>
                  <Label htmlFor="infra-ssl-cert-path">{t("infra.ssl_cert_path")}</Label>
                  <Input id="infra-ssl-cert-path" name="input-3" type="text" value={certPath} onChange={e => setCertPath(e.target.value)} placeholder="/etc/letsencrypt/live/c2.example.com/fullchain.pem" className="font-mono text-xs" />
                </div>
                <div>
                  <Label htmlFor="infra-ssl-key-path">{t("infra.ssl_key_path")}</Label>
                  <Input id="infra-ssl-key-path" name="input-4" type="text" value={keyPath} onChange={e => setKeyPath(e.target.value)} placeholder="/etc/letsencrypt/live/c2.example.com/privkey.pem" className="font-mono text-xs" />
                </div>
              </div>
              <div className="flex items-center gap-4">
                <Label className="flex items-center gap-2 cursor-pointer">
                  <Checkbox checked={wsSupport} onCheckedChange={(v) => setWsSupport(!!v)} />
                  <span className="text-xs text-muted-foreground">{t("infra.ws_support")}</span>
                </Label>
                <Label className="flex items-center gap-2 cursor-pointer">
                  <Input value={extC2Path} onChange={(e) => setExtC2Path(e.target.value)} placeholder={t("infra.ext_c2_placeholder")} className="text-xs" />
                  <span className="text-xs text-muted-foreground">{t("infra.ext_c2_path")}</span>
                </Label>
              </div>
            </CardContent>
          </Card>

          <Card className="gap-0 overflow-hidden py-0">
            <CardHeader
              icon={FileUp}
              tone="warning"
              title={t("infra.generate_config")}
              description={t("infra.generate_desc")}
            />
            <CardContent className="p-(--card-spacing)">
              <div className="flex flex-wrap gap-2 mb-4">
                <Button onClick={() => generateConfig("nginx")} className="bg-success hover:bg-success text-success-foreground">
                  <Server className="size-4" /> Nginx
                </Button>
                <Button onClick={() => generateConfig("apache")} className="bg-warning-solid hover:bg-warning-solid/90 text-warning-foreground">
                  <Server className="size-4" /> Apache
                </Button>
                <Button onClick={() => generateConfig("haproxy")} className="bg-destructive-solid hover:bg-destructive-solid/90 text-destructive-foreground">
                  <Server className="size-4" /> HAProxy
                </Button>
              </div>
              {generating && <div className="text-muted-foreground text-sm"><Spinner size="sm" /> {t("infra.generating")}</div>}
              {configOutput && !generating && (
                <div>
                  <div className="flex items-center justify-between mb-2">
                    <span className="text-xs font-medium text-muted-foreground">{t("infra.generated_config")} ({configType.toUpperCase()})</span>
                    <div className="flex gap-2">
                      <Button onClick={copyConfig} variant="outline" size="sm">
                        <Copy className="size-4" /> {copied ? t("infra.copied") : t("common.copy")}
                      </Button>
                      <Button onClick={downloadConfig} variant="outline" size="sm">
                        <Download className="size-4" /> {t("common.download")}
                      </Button>
                    </div>
                  </div>
                   <pre className="bg-card text-chart-1 p-4 rounded-xl text-xs overflow-auto max-h-[400px] whitespace-pre-wrap font-mono leading-relaxed select-all border border-border">{configOutput}</pre>
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="redirectors" className="mt-0">
          <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
            <div className="lg:col-span-2">
              <Card className="gap-0 overflow-hidden py-0">
                <CardHeader
                  icon={Share2}
                  tone="primary"
                  title={t("infra.redirectors")}
                  description={t("infra.redirectors_desc")}
                  action={
                    <Button onClick={() => { setEditingRd(null); setRdName(""); setRdHost(""); setRdType("nginx"); setRdSSHUser("root"); setRdSSHPort(22); setRdSSHKey(""); setRdSSHPassword(""); setRdConfig(""); }}>
                      <Plus className="size-4" /> {t("infra.add_redirector")}
                    </Button>
                  }
                />
                <CardContent className="p-(--card-spacing)">
                  {redirectors.length === 0 ? (
                      <EmptyState icon={Share2} title={t("infra.no_redirectors")} message={t("infra.no_redirectors_hint")} />
                  ) : (
                    <div className="space-y-3">
                      {redirectors.map(rd => (
                        <div key={rd.id} className="flex items-center justify-between p-4">
                          <Card className="bg-muted w-full">
                            <CardContent>
                              <div className="flex items-center justify-between">
                                <div className="flex items-center gap-4">
                                   <StatusDot tone={rd.status === "active" ? "success" : rd.status === "error" ? "destructive" : "muted"} size="md" pulse={rd.status === "active"} />
                                  <div>
                                    <div className="text-sm font-medium text-foreground">{rd.name}</div>
                                    <div className="text-xs text-muted-foreground mt-0.5">
                                      {rd.host} &middot; {rd.type.toUpperCase()} &middot; SSH: {rd.ssh_user}@{rd.host}:{rd.ssh_port}
                                    </div>
                                  </div>
                                </div>
                                <div className="flex items-center gap-2">
                                  <Badge variant={rd.status === "active" ? "success" : rd.status === "error" ? "destructive" : "secondary"} className="text-xs px-2 py-0.5 rounded-full font-medium">{rd.status}</Badge>
                                  <Button onClick={() => {
                                    setEditingRd(rd.id); setRdName(rd.name); setRdHost(rd.host); setRdType(rd.type);
                                    setRdSSHUser(rd.ssh_user || "root"); setRdSSHPort(rd.ssh_port || 22);
                                    setRdConfig(rd.config || "");
                                  }} variant="outline" size="sm">
                                    <Pencil className="size-4" />{t("common.edit")}
                                  </Button>
                                  <Button onClick={async () => {
                                    if (!(await confirm({ message: t("infra.delete_redirector_confirm") }))) return;
                                    setDeleting(rd.id);
                                    try { await removeRedirector.mutate(rd.id); } finally { setDeleting(null); }
                                  }} variant="destructive" size="sm">
                                    {deleting === rd.id ? <Spinner size="sm" className="mr-1" /> : <Trash2 className="size-4 mr-1" />}{deleting === rd.id ? t("infra.deleting") : t("common.delete")}
                                  </Button>
                                  <Button onClick={() => { setRdGenerateHost(rd.host); setDeployResult(null); setShowGenModal(true); }} size="sm">
                                    <Rocket className="size-4" />{t("infra.deploy")}
                                  </Button>
                                </div>
                              </div>
                            </CardContent>
                          </Card>
                        </div>
                      ))}
                    </div>
                  )}
                </CardContent>
              </Card>
            </div>

            <div>
              <Card className="gap-0 overflow-hidden py-0">
                <CardHeader
                  icon={Server}
                  tone="success"
                  title={editingRd ? t("infra.edit_redirector") : t("infra.new_redirector")}
                />
                <CardContent className="p-(--card-spacing) space-y-4">
                  <div>
                    <Label htmlFor="common-name">{t("common.name")}</Label>
                    <Input id="common-name" name="input-7" type="text" value={rdName} onChange={e => setRdName(e.target.value)} placeholder={t("infra.name_label")} />
                  </div>
                  <div>
                    <Label htmlFor="infra-host">{t("infra.host")}</Label>
                    <Input id="infra-host" name="input-8" type="text" value={rdHost} onChange={e => setRdHost(e.target.value)} placeholder="192.168.1.100" />
                  </div>
                  <div>
                    <Label htmlFor="common-type">{t("common.type")}</Label>
                    <Select id="common-type" value={rdType} onValueChange={(v) => { if (v) setRdType(v); }}>
                      <SelectTrigger className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="nginx">Nginx</SelectItem>
                        <SelectItem value="apache">Apache</SelectItem>
                        <SelectItem value="haproxy">HAProxy</SelectItem>
                        <SelectItem value="caddy">Caddy</SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <Label htmlFor="infra-ssh-user">{t("infra.ssh_user")}</Label>
                      <Input id="infra-ssh-user" name="input-10" type="text" value={rdSSHUser} onChange={e => setRdSSHUser(e.target.value)} placeholder="root" />
                    </div>
                    <div>
                      <Label htmlFor="infra-ssh-port">{t("infra.ssh_port")}</Label>
                      <Input id="infra-ssh-port" name="input-11" type="number" value={rdSSHPort} onChange={e => setRdSSHPort(Number(e.target.value))} />
                    </div>
                  </div>
                  <div>
                    <Label htmlFor="infra-ssh-key">{t("infra.ssh_key")}</Label>
                    <Textarea id="infra-ssh-key" name="textarea-12" value={rdSSHKey} onChange={e => setRdSSHKey(e.target.value)} placeholder={"-----BEGIN OPENSSH PRIVATE KEY-----\n..."} rows={3} className="font-mono text-xs" />
                  </div>
                  <div>
                    <Label htmlFor="infra-ssh-password">{t("infra.ssh_password")}</Label>
                    <Input id="infra-ssh-password" name="input-13" type="password" value={rdSSHPassword} onChange={e => setRdSSHPassword(e.target.value)} placeholder="password" />
                  </div>
                  <div className="flex gap-2">
                    <Button onClick={() => { void saveRedirector.mutate(); }} disabled={saveRedirector.isPending} className="flex-1">
                      <Save className="size-4" /> {saveRedirector.isPending ? t("infra.saving") : editingRd ? t("infra.update") : t("common.save")}
                    </Button>
                    <Button onClick={() => { void testSSH.mutate(); }} disabled={testSSH.isPending} variant="outline" aria-label={t("infra.test_ssh")}>
                      {testSSH.isPending ? <Spinner size="sm" /> : <Plug className="size-4" />}
                    </Button>
                  </div>
                </CardContent>
              </Card>
            </div>
          </div>

          <Dialog open={showGenModal} onOpenChange={setShowGenModal}>
            <DialogContent size="2xl" className="max-h-[85vh] overflow-y-auto p-6">
              <DialogHeader>
                <DialogTitle>{t("infra.deploy_redirector_title")}</DialogTitle>
              </DialogHeader>

              <div className="grid grid-cols-2 gap-3">
                <div>
                  <Label htmlFor="infra-domain-2">{t("infra.domain")}</Label>
                  <Input id="infra-domain-2" name="input-14" type="text" value={rdGenerateDomain} onChange={e => setRdGenerateDomain(e.target.value)} placeholder="c2.example.com" />
                </div>
                <div>
                  <Label htmlFor="infra-backend-url">{t("infra.backend_url")}</Label>
                  <Input id="infra-backend-url" name="input-15" type="text" value={backendURL} onChange={e => setBackendURL(e.target.value)} placeholder="https://127.0.0.1:8080" />
                </div>
                <div>
                  <Label htmlFor="infra-listen-port-2">{t("infra.listen_port")}</Label>
                  <Input id="infra-listen-port-2" name="input-16" type="number" value={rdGeneratePort} onChange={e => setRdGeneratePort(Number(e.target.value))} />
                </div>
                <div className="flex items-center gap-4 pt-6">
                  <Label className="flex items-center gap-2 cursor-pointer">
                    <Checkbox checked={rdGenerateTLS} onCheckedChange={(v) => setRdGenerateTLS(!!v)} />
                    <span className="text-xs text-muted-foreground">{t("infrastructure.tls")}</span>
                  </Label>
                  <Label className="flex items-center gap-2 cursor-pointer">
                    <Checkbox checked={rdGenerateWS} onCheckedChange={(v) => setRdGenerateWS(!!v)} />
                    <span className="text-xs text-muted-foreground">{t("infrastructure.websocket")}</span>
                  </Label>
                </div>
              </div>

              <div className="flex gap-2">
                <Button onClick={() => { void generateRedirector.mutate(); }} disabled={generateRedirector.isPending}>
                  {generateRedirector.isPending ? <Spinner size="sm" className="mr-1" /> : <WandSparkles className="size-4 mr-1" />} {generateRedirector.isPending ? t("infra.generating") : t("infra.generate_config")}
                </Button>
                <Button disabled={!rdConfig || deploySSH.isPending} onClick={() => { setDeployResult(null); void deploySSH.mutate(); }}>
                  {deploySSH.isPending ? <Spinner size="xs" className="mr-1" /> : <Rocket className="size-4" />} {deploySSH.isPending ? t("infra.deploying") : t("infra.deploy_via_ssh")}
                </Button>
              </div>

              {rdConfig && (
                <div>
                  <Label>{t("infra.generated_config")} ({rdType.toUpperCase()})</Label>
                   <pre className="bg-card text-chart-1 p-4 rounded-xl text-xs overflow-auto max-h-[300px] whitespace-pre-wrap font-mono leading-relaxed border border-border">{rdConfig}</pre>
                </div>
              )}

              {deployResult && (
                <Banner tone={deployResult.success ? "success" : "destructive"} icon={deployResult.success ? <CheckCircle className="size-4" /> : <CircleAlert className="size-4" />} className="mb-4">
                  <div className="font-medium">{deployResult.message}</div>
                  {deployResult.stdout && <pre className="text-xs mt-1 text-muted-foreground whitespace-pre-wrap">{deployResult.stdout}</pre>}
                  {deployResult.stderr && <pre className="text-xs mt-1 text-muted-foreground whitespace-pre-wrap">{deployResult.stderr}</pre>}
                </Banner>
              )}
            </DialogContent>
          </Dialog>
        </TabsContent>

        <TabsContent value="acme" className="mt-0">
        <Card className="gap-0 overflow-hidden py-0">
          <CardHeader
            icon={Award}
            tone="info"
            title={t("infra.acme_title")}
            description={t("infra.acme_desc")}
          />
          <CardContent className="p-(--card-spacing) space-y-4">
            <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
              <div>
                <Label><Globe className="size-4" />{t("infra.domain")}</Label>
                <Input aria-label="c2.example.com" name="input-19" type="text" value={acmeDomain} onChange={e => setAcmeDomain(e.target.value)} placeholder="c2.example.com" />
              </div>
              <div>
                <Label><Mail className="size-4" />{t("infra.email")}</Label>
                <Input aria-label="admin@example.com" name="input-20" type="email" value={acmeEmail} onChange={e => setAcmeEmail(e.target.value)} placeholder="admin@example.com" />
              </div>
              <div>
                <Label><Plug className="size-4" />{t("infra.http01_port")}</Label>
                <Input aria-label={t("infra.http01_port")} name="input-21" type="number" value={acmePort} onChange={e => setAcmePort(Number(e.target.value))} />
              </div>
            </div>
            <div className="flex items-center gap-4">
              <Label className="flex items-center gap-2 cursor-pointer">
                <Checkbox checked={acmeStaging} onCheckedChange={(v) => setAcmeStaging(!!v)} />
                <span className="text-xs text-muted-foreground">{t("infra.use_staging")}</span>
              </Label>
              <Button onClick={provisionCert} disabled={acmeProvisioning}>
                {acmeProvisioning ? <Spinner size="xs" /> : <Award className="size-4" />} {acmeProvisioning ? t("infra.provisioning") : t("infra.auto_provision")}
              </Button>
            </div>
            {certPath && keyPath && (
              <div className="p-3 bg-success/15 rounded-lg text-xs text-success-ink">
                <CheckCircle className="size-4" />
                <b>{t("infra.cert_success")}</b> {t("infra.cert_set_to")} {certPath}
              </div>
            )}
          </CardContent>
        </Card>
      </TabsContent>

      <TabsContent value="export" className="mt-0">
        <Card className="gap-0 overflow-hidden py-0">
          <CardHeader
            icon={FileUp}
            tone="primary"
            title={t("infra.c2_profile_export")}
            description={t("infra.c2_profile_export_desc")}
          />
          <CardContent className="p-(--card-spacing) space-y-4">
            <div className="mb-4">
              <Label className="mb-2 block">{t("infra.export_format")}</Label>
              <div className="flex gap-3">
                {([
                  { value: "json", label: "JSON", icon: <Code className="size-4" />, active: "border-primary bg-primary/10 text-primary" },
                  { value: "nginx", label: "Nginx", icon: <Server className="size-4" />, active: "border-success bg-success/15 text-success-ink" },
                  { value: "env", label: "ENV", icon: <Terminal className="size-4" />, active: "border-secondary bg-secondary/50 text-muted-foreground" },
                ] as const).map((opt) => (
                  <Button key={opt.value} onClick={() => setExportFormat(opt.value)}
                    variant={exportFormat === opt.value ? "default" : "outline"}
                    className={exportFormat === opt.value ? opt.active : ""}>
                    {opt.icon}{opt.label}
                  </Button>
                ))}
              </div>
            </div>
            <Button onClick={exportProfile} disabled={exporting} className="bg-primary hover:bg-primary/90 text-primary-foreground">
              {exporting ? <Spinner size="sm" /> : <Download className="size-4" />} {exporting ? t("infra.exporting") : `${t("infra.export_as")} ${exportFormat.toUpperCase()}`}
            </Button>
            <Card className="bg-muted">
              <CardContent>
                <Info className="size-4" />
                <b>{t("infra.about")}</b> {t("infra.about_desc")}
              </CardContent>
            </Card>
          </CardContent>
        </Card>
      </TabsContent>
      </Tabs>
      {modal}
      </DataState>
    </PageContainer>
  );
}

