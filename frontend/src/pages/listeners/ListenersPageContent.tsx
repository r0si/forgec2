
import { registerFragment } from "@/lib/i18n";
import { enGenerate } from "@/lib/i18n/fragments/en.generate";
registerFragment(enGenerate);

import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { z } from "zod";
import { useI18n } from "@/lib/i18n";
import { useForm } from "@/lib/hooks/useForm";
import { Field } from "@/components/ui/field";
import { EmptyState } from "@/components/ui/empty-state";
import { PageContainer } from "@/components/ui/page-container";
import { PageToolbar } from "@/components/ui/page-toolbar";
import { MetricGrid } from "@/components/ui/metric-grid";
import { StatusBadge } from "@/components/ui/status-indicator";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { CopyButton } from "@/components/ui/copy-button";
import { DataError } from "@/components/ui/data-state";
import { Badge } from "@/components/ui/badge";
import { StatTile } from "@/components/ui/stat-tile";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { Checkbox } from "@/components/ui/checkbox";
import { ArrowLeftRight, Check, Copy, Info, Pencil, Plug, Plus, Power, SlidersHorizontal, Trash2, Radio, Globe, Network, Flame, AlertTriangle } from "lucide-react";
import type { Listener } from "./components/types";
import { emptyCreateForm, emptyEditForm, isTLSScheme } from "./components/types";
import { useListenersData } from "./components/useListenersData";
import type { CreateListenerForm, EditListenerForm } from "./components/types";
import { ListenerHealthCell } from "./components/ListenerHealthCell";
import { healthForListener, isProblemHealth } from "@/lib/listener-health";

export default function ListenersPageContent() {
  const { t } = useI18n();
  const {
    listeners,
    loading,
    error,
    setError,
    agentCountMap,
    agentCountError,
    healthByTarget,
    healthLoading,
    healthError,
    healthHistory,
    creating,
    createListener,
    updateListener,
    toggleListener,
    deleteListener,
    resetHealth,
  } = useListenersData();
  const [typeFilter, setTypeFilter] = useState("");
  const [statusFilter, setStatusFilter] = useState("");
  const [healthFilter, setHealthFilter] = useState("");
  const [showCreate, setShowCreate] = useState(false);

  const portSchema = useMemo(
    () =>
      z
        .string()
        .trim()
        .min(1, t("generate.toast.port_required"))
        .refine(
          (v) => /^\d+$/.test(v) && Number(v) >= 1 && Number(v) <= 65535,
          t("listeners.port_invalid"),
        ),
    [t],
  );

  const createSchema = useMemo(
    () =>
      z.object({
        name: z.string().trim().min(1, t("generate.toast.listener_name_required")),
        type: z.string().min(1),
        host: z.string().trim().min(1, t("generate.toast.host_required")),
        port: portSchema,
        protocol: z.string(),
        tags: z.string(),
        color: z.string(),
        client_ca_file: z.string(),
        require_client_cert: z.boolean(),
        min_tls_version: z.string(),
      }),
    [portSchema, t],
  );

  const editSchema = useMemo(
    () =>
      createSchema.extend({
        notes: z.string(),
      }),
    [createSchema],
  );

  const typeChange = (setFieldValue: (f: keyof CreateListenerForm, v: unknown) => void) => (type: string | null) => {
    const val = type ?? "http";
    setFieldValue("type", val);
    setFieldValue("protocol", val === "https" ? "https" : val);
  };

  // Shared per-listener TLS posture fields for the create/edit dialogs. Only
  // rendered for https/tls schemes: the server refuses the knobs on any other
  // scheme, and these can only tighten the server-wide TLS posture.
  const tlsFields = (
    prefix: string,
    values: { client_ca_file: string; require_client_cert: boolean; min_tls_version: string },
    setField: (f: keyof CreateListenerForm, v: unknown) => void,
  ) => (
    <div className="space-y-3 rounded-md border border-border bg-muted/20 p-3">
      <p className="text-xs font-semibold text-muted-foreground">{t("listeners.tls_section")}</p>
      <Field label={t("listeners.field_client_ca")} htmlFor={`${prefix}-client-ca`} hint={t("listeners.client_ca_hint")}>
        <Input id={`${prefix}-client-ca`} className="font-mono text-xs" value={values.client_ca_file}
          onChange={(e) => setField("client_ca_file", e.target.value)}
          placeholder={t("listeners.client_ca_ph")} />
      </Field>
      <Label className="flex items-center gap-2 text-foreground">
        <Checkbox checked={values.require_client_cert}
          onCheckedChange={(v) => setField("require_client_cert", v === true)} />
        {t("listeners.field_require_client_cert")}
      </Label>
      <Field label={t("listeners.field_min_tls")} htmlFor={`${prefix}-min-tls`}>
        <Select value={values.min_tls_version} onValueChange={(val) => setField("min_tls_version", val ?? "")}>
          <SelectTrigger id={`${prefix}-min-tls`} className="w-full">
            <SelectValue placeholder={t("listeners.min_tls_inherit")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="">{t("listeners.min_tls_inherit")}</SelectItem>
            <SelectItem value="1.2">TLS 1.2</SelectItem>
            <SelectItem value="1.3">TLS 1.3</SelectItem>
          </SelectContent>
        </Select>
      </Field>
      <p className="text-micro-sm text-muted-foreground">{t("listeners.tls_hint")}</p>
    </div>
  );

  const [editingListener, setEditingListener] = useState<Listener | null>(null);
  const [showEdit, setShowEdit] = useState(false);
  const { confirm, modal } = useConfirm();

  const {
    values: createForm,
    errors: createErrors,
    touched: createTouched,
    isSubmitting: createSubmitting,
    isValid: createValid,
    setFieldValue: createSetField,
    handleChange: createChange,
    handleBlur: createBlur,
    handleSubmit: handleCreateSubmit,
    resetForm: resetCreate,
  } = useForm<CreateListenerForm>({
    initialValues: emptyCreateForm(),
    schema: createSchema,
    onSubmit: async (values) => {
      const ok = await createListener(values);
      if (ok) {
        setShowCreate(false);
        resetCreate();
      }
    },
  });

  const {
    values: editForm,
    errors: editErrors,
    touched: editTouched,
    isSubmitting: editSubmitting,
    isValid: editValid,
    setFieldValue: setEditChange,
    handleChange: handleEditChange,
    handleBlur: handleEditBlur,
    handleSubmit: handleEditSubmit,
    resetForm: resetEdit,
  } = useForm<EditListenerForm>({
    initialValues: emptyEditForm(),
    schema: editSchema,
    onSubmit: async (values) => {
      const id = editingListener?.id || editingListener?.ID || "";
      const ok = await updateListener(id, values);
      if (ok) {
        setShowEdit(false);
        setEditingListener(null);
      }
    },
  });

  const handleEdit = (listener: Listener) => {
    setEditingListener(listener);
    resetEdit({
      name: listener.name || "",
      type: listener.type || "http",
      host: listener.host || "",
      port: String(listener.port ?? ""),
      protocol: listener.scheme || listener.protocol || listener.type || "http",
      notes: listener.notes || "",
      tags: listener.tags || "",
      color: listener.color || "",
      client_ca_file: listener.client_ca_file || "",
      require_client_cert: listener.require_client_cert === true,
      min_tls_version: listener.min_tls_version || "",
    });
    setShowEdit(true);
  };

  const handleToggle = async (listener: Listener) => {
    await toggleListener(listener);
  };

  const handleDelete = async (listener: Listener) => {
    const id = listener.id || "";
    if (!id) return;
    const name = listener.name || listener.type || "";
    if (!(await confirm({ message: t("listeners.confirm_delete"), requireText: name }))) return;
    await deleteListener(id);
  };

  const { total, enabledCount, httpCount, tcpCount, dnsCount, burnedCount, filtered } = useMemo(() => {
    const total = listeners.length;
    const enabledCount = listeners.filter((l) => l.enabled === true).length;
    const httpCount = listeners.filter((l) => l.type === "http").length;
    const tcpCount = listeners.filter((l) => l.type === "tcp").length;
    const dnsCount = listeners.filter((l) => l.type === "dns").length;
    const burnedCount = listeners.filter((l) => healthForListener(healthByTarget, l.id || l.ID)?.status === "burned").length;
    const filtered = listeners.filter((l) => {
      const type = l.type || "";
      if (typeFilter && type !== typeFilter) return false;
      const enabled = l.enabled === true;
      if (statusFilter === "enabled" && !enabled) return false;
      if (statusFilter === "disabled" && enabled) return false;
      const health = healthForListener(healthByTarget, l.id || l.ID);
      if (healthFilter === "problem" && !isProblemHealth(health)) return false;
      if (healthFilter === "burned" && health?.status !== "burned") return false;
      return true;
    });
    return { total, enabledCount, httpCount, tcpCount, dnsCount, burnedCount, filtered };
  }, [listeners, healthByTarget, typeFilter, statusFilter, healthFilter]);

  return (
    <PageContainer
      title={t("listeners.title")}
      subtitle={t("listeners.subtitle")}
      actions={
        <>
          {/* The circuit-breaker config form used to be duplicated in a dialog
              here and on the /circuit-breaker page; both POSTed the same
              payload to paths.circuitBreaker.config. The page owns it, and it
              also carries the reset/toggle actions and event history that the
              dialog never had. */}
          <Button variant="outline" render={<Link to="/circuit-breaker" />}>
            <SlidersHorizontal className="size-4" />
            <span>{t("listeners.breaker_config")}</span>
          </Button>
          <Button onClick={() => setShowCreate(true)}>
            <Plus className="size-4" />
            <span>{t("listeners.create_listener")}</span>
          </Button>
        </>
      }
    >
      {error && (
        <DataError
          message={error}
          onDismiss={() => setError(null)}
          className="mb-4"
        />
      )}

      {healthError && (
        <div role="alert" className="mb-4 flex flex-wrap items-center gap-3 rounded-xl border border-warning/30 bg-warning/10 px-4 py-3 text-xs text-warning-ink">
          <AlertTriangle className="size-4 shrink-0" aria-hidden="true" />
          <span className="min-w-0 flex-1">{t("listeners.health_unavailable", { message: healthError })}</span>
        </div>
      )}

      {agentCountError && (
        <div role="alert" className="mb-4 flex flex-wrap items-center gap-3 rounded-xl border border-warning/30 bg-warning/10 px-4 py-3 text-xs text-warning-ink">
          <AlertTriangle className="size-4 shrink-0" aria-hidden="true" />
          <span className="min-w-0 flex-1">{t("listeners.agent_counts_unavailable", { message: agentCountError })}</span>
        </div>
      )}

      <MetricGrid count={6}>
        <Card interactive className="p-(--card-spacing)"><StatTile label={t("listeners.total")} value={loading ? "…" : total} icon={<Plug className="size-5" />} /></Card>
        <Card interactive className="p-(--card-spacing)"><StatTile label={t("listeners.running")} value={loading ? "…" : enabledCount} tone="success" icon={<Power className="size-5" />} trend={enabledCount === total && total > 0 ? t("dashboard.all_online") : undefined} /></Card>
        <Card interactive className="p-(--card-spacing)"><StatTile label={t("listeners.burned")} value={loading || healthLoading ? "…" : burnedCount} tone={burnedCount > 0 ? "destructive" : "muted"} icon={<Flame className="size-5" />} />{!loading && !healthLoading && healthError && <p className="mt-1 text-micro-sm text-warning-ink">{t("listeners.health_unknown_burned")}</p>}</Card>
        <Card interactive className="p-(--card-spacing)"><StatTile label={t("listeners.http")} value={loading ? "…" : httpCount} icon={<Globe className="size-5" />} /></Card>
        <Card interactive className="p-(--card-spacing)"><StatTile label={t("listeners.tcp")} value={loading ? "…" : tcpCount} icon={<Network className="size-5" />} /></Card>
        <Card interactive className="p-(--card-spacing)"><StatTile label={t("listeners.dns")} value={loading ? "…" : dnsCount} icon={<Radio className="size-5" />} /></Card>
      </MetricGrid>

      <PageToolbar sticky>
        <div className="flex gap-2">
          <Select value={typeFilter} onValueChange={(val) => setTypeFilter(typeof val === "string" ? val : "")}>
            <SelectTrigger className="flex-1 sm:w-40">
              <SelectValue placeholder={t("listeners.all_types")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">{t("listeners.all_types")}</SelectItem>
              <SelectItem value="http">HTTP</SelectItem>
              <SelectItem value="https">HTTPS</SelectItem>
              <SelectItem value="tcp">TCP</SelectItem>
              <SelectItem value="dns">DNS</SelectItem>
              <SelectItem value="icmp">ICMP</SelectItem>
              <SelectItem value="ssh">SSH</SelectItem>
              <SelectItem value="h2c">H2C</SelectItem>
              <SelectItem value="udp">UDP</SelectItem>
              <SelectItem value="quic">QUIC</SelectItem>
            </SelectContent>
          </Select>
          <Select value={statusFilter} onValueChange={(val) => setStatusFilter(typeof val === "string" ? val : "")}>
            <SelectTrigger className="flex-1 sm:w-40">
              <SelectValue placeholder={t("listeners.all_statuses")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">{t("listeners.all_statuses")}</SelectItem>
              <SelectItem value="enabled">{t("listeners.enabled")}</SelectItem>
              <SelectItem value="disabled">{t("listeners.disabled")}</SelectItem>
            </SelectContent>
          </Select>
          <Select value={healthFilter} onValueChange={(val) => setHealthFilter(typeof val === "string" ? val : "")}>
            <SelectTrigger className="flex-1 sm:w-44">
              <SelectValue placeholder={t("listeners.filter_health_all")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">{t("listeners.filter_health_all")}</SelectItem>
              <SelectItem value="problem">{t("listeners.filter_health_problem")}</SelectItem>
              <SelectItem value="burned">{t("listeners.health_burned")}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </PageToolbar>

      <Card className="overflow-hidden">
        <Table label={t("listeners.title")}>
          <TableHeader className="bg-card/95 backdrop-blur supports-[backdrop-filter]:bg-card/90 sticky top-0 z-10 border-b border-border">
            <TableRow className="hover:bg-transparent">
              <TableHead className="text-left py-3 px-4 sm:py-4 sm:px-6 font-medium text-muted-foreground min-w-[120px]">{t("listeners.col_name")}</TableHead>
              <TableHead className="max-lg:hidden text-left py-3 px-3 sm:py-4 sm:px-4 font-medium text-muted-foreground min-w-[80px]">{t("listeners.col_type")}</TableHead>
              <TableHead className="max-lg:hidden text-left py-3 px-3 sm:py-4 sm:px-4 font-medium text-muted-foreground min-w-[100px]">{t("listeners.col_tags")}</TableHead>
              <TableHead className="max-md:hidden text-left py-3 px-3 sm:py-4 sm:px-4 font-medium text-muted-foreground min-w-[160px]">{t("listeners.col_address")}</TableHead>
              <TableHead className="max-lg:hidden text-left py-3 px-3 sm:py-4 sm:px-4 font-medium text-muted-foreground min-w-[60px]">{t("listeners.col_agents")}</TableHead>
              <TableHead className="text-left py-3 px-3 sm:py-4 sm:px-4 font-medium text-muted-foreground min-w-[80px]">{t("listeners.col_status")}</TableHead>
              <TableHead className="text-left py-3 px-3 sm:py-4 sm:px-4 font-medium text-muted-foreground min-w-[120px]">{t("listeners.col_health")}</TableHead>
              <TableHead className="max-2xl:hidden text-left py-3 px-3 sm:py-4 sm:px-4 font-medium text-muted-foreground min-w-[120px]">{t("listeners.col_notes")}</TableHead>
              <TableHead className="sticky-end text-right py-3 px-4 sm:py-4 sm:px-6 font-medium text-muted-foreground min-w-[160px]">{t("listeners.col_actions")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody className="divide-y divide-border">
            {loading ? (
              [1, 2, 3, 4, 5].map(i => (
                <TableRow key={i}>
                  <TableCell className="py-3 px-4 sm:py-4 sm:px-6"><Skeleton className="h-4 w-24" /></TableCell>
                  <TableCell className="max-lg:hidden py-3 px-3 sm:py-4 sm:px-4"><Skeleton className="h-4 w-14" /></TableCell>
                  <TableCell className="max-lg:hidden py-3 px-3 sm:py-4 sm:px-4"><Skeleton className="h-4 w-20" /></TableCell>
                  <TableCell className="max-md:hidden py-3 px-3 sm:py-4 sm:px-4"><Skeleton className="h-4 w-32" /></TableCell>
                  <TableCell className="max-lg:hidden py-3 px-3 sm:py-4 sm:px-4"><Skeleton className="h-4 w-16" /></TableCell>
                  <TableCell className="py-3 px-3 sm:py-4 sm:px-4"><Skeleton className="h-4 w-20" /></TableCell>
                  <TableCell className="py-3 px-3 sm:py-4 sm:px-4"><Skeleton className="h-4 w-24" /></TableCell>
                  <TableCell className="sticky-end py-3 px-4 sm:py-4 sm:px-6 text-right"><div className="flex items-center justify-end gap-1 sm:gap-2"><Skeleton className="h-8 w-20 rounded-lg" /><Skeleton className="h-8 w-20 rounded-lg" /><Skeleton className="h-8 w-20 rounded-lg" /><Skeleton className="h-8 w-20 rounded-lg" /></div></TableCell>
                </TableRow>
              ))
            ) : filtered.length > 0 ? (
              filtered.map(l => {
                const id = String(l.id || l.ID || "");
                const name = l.name || "";
                const type = l.type || "";
                const scheme = l.scheme || l.protocol || type;
                const host = l.host || "";
                const port = l.port ?? "";
                const enabled = l.enabled === true;
                const notes = l.notes || "-";
                const tags = l.tags || "";
                const tagList = tags.split(",").map(t => t.trim()).filter(Boolean);
                const health = healthForListener(healthByTarget, id);
                return (
                  <TableRow key={id} className="hover:bg-muted/50 transition-colors">
                    <TableCell className="py-3 px-4 sm:py-4 sm:px-6 font-medium"><Link to={`/listeners/${id}`} className="text-primary hover:underline">{name}</Link></TableCell>
                  <TableCell className="max-lg:hidden py-3 px-3 sm:py-4 sm:px-4">
                      <Badge variant={type === "http" ? "outline" : "success"} className="text-xs-sm font-medium">{type}</Badge>
                    </TableCell>
                    <TableCell className="max-lg:hidden py-3 px-3 sm:py-4 sm:px-4">
                      <div className="flex flex-wrap gap-1">
                        {tagList.length > 0 ? tagList.map((tag) => (
                          <Badge key={tag} variant="secondary" className="text-micro-sm font-medium">{tag}</Badge>
                        )) : <span className="text-xs text-muted-foreground">-</span>}
                      </div>
                    </TableCell>
                    <TableCell className="max-md:hidden py-3 px-3 sm:py-4 sm:px-4 font-mono text-xs text-muted-foreground">{scheme}://{host}:{port}</TableCell>
                    <TableCell className="max-lg:hidden py-3 px-3 sm:py-4 sm:px-4 text-center">
                      <span className="text-xs font-mono text-muted-foreground">{agentCountError ? t("status.unknown") : (agentCountMap[id] ?? 0)}</span>
                    </TableCell>
                    <TableCell className="py-3 px-3 sm:py-4 sm:px-4">
                      <StatusBadge status={enabled ? "online" : "offline"} />
                    </TableCell>
                    <TableCell className="py-3 px-3 sm:py-4 sm:px-4">
                      <ListenerHealthCell
                        health={health}
                        samples={healthHistory[id]}
                        onReset={health ? () => void resetHealth(id) : undefined}
                      />
                    </TableCell>
                    <TableCell className="max-2xl:hidden py-3 px-3 sm:py-4 sm:px-4 text-xs text-muted-foreground max-w-[150px] truncate">{notes}</TableCell>
                     <TableCell className="sticky-end py-3 px-4 sm:py-4 sm:px-6 text-right">
                       <div className="flex items-center justify-end gap-1 sm:gap-2">
                         <CopyButton
                            text={`${scheme}://${host}:${port}`}
                            title={t("listeners.copy")}
                            size="icon-xs"
                            className="size-9 sm:px-3 sm:py-1 sm:w-auto sm:h-auto text-xs bg-secondary hover:bg-secondary/80 rounded-lg flex items-center justify-center gap-x-1 text-muted-foreground"
                          >
                            {(copied) => (
                              <>
                                {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
                                <span className="hidden sm:inline">{t("listeners.copy")}</span>
                              </>
                            )}
                          </CopyButton>
                         <Button variant="ghost" onClick={() => handleEdit(l)} className="size-9 sm:px-3 sm:py-1 sm:w-auto sm:h-auto text-xs bg-secondary/70 hover:bg-secondary text-foreground rounded-lg flex items-center justify-center gap-x-1">
                            <Pencil className="size-4" />
                            <span className="hidden sm:inline">{t("listeners.edit")}</span>
                         </Button>
                         <Button variant="ghost" onClick={() => handleToggle(l)} className={`size-9 sm:px-3 sm:py-1 sm:w-auto sm:h-auto text-xs ${enabled ? "bg-warning/15 text-warning-ink hover:bg-warning/15" : "bg-success/15 text-success-ink hover:bg-success/15"} rounded-lg flex items-center justify-center gap-x-1`}>
                            <Power className="size-4" />
                               <span className="hidden sm:inline">{enabled ? t("listeners.stop") : t("listeners.start")}</span>
                         </Button>
                          <Button variant="ghost" onClick={() => handleDelete(l)} className="size-9 sm:px-3 sm:py-1 sm:w-auto sm:h-auto text-xs bg-destructive/10 hover:bg-destructive/20 text-destructive-ink rounded-lg flex items-center justify-center gap-x-1">
                           <Trash2 className="size-4" />
                             <span className="hidden sm:inline">{t("listeners.delete")}</span>
                         </Button>
                       </div>
                     </TableCell>
                  </TableRow>
                );
              })
            ) : (
              <TableRow>
                <TableCell colSpan={9} className="py-12 text-center text-muted-foreground">
                  <EmptyState icon={Plug} title={t("listeners.empty_title")} message={t("listeners.empty_message")} />
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
        <div className="sm:hidden px-4 py-2 text-center text-xs text-muted-foreground border-t border-border bg-muted">
          <ArrowLeftRight className="size-4" /> {t("listeners.swipe_more")}
        </div>
      </Card>

      <div className="mt-4 sm:mt-6 p-3 sm:p-4 bg-primary/10 border border-primary/20 rounded-xl text-xs text-primary">
        <Info className="size-4" />
        <strong>{t("listeners.tip")}</strong> {t("listeners.tip_text")}
      </div>

      <Dialog open={showCreate} onOpenChange={(open) => { setShowCreate(open); if (open) resetCreate(); }}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t("listeners.create_dialog_title")}</DialogTitle>
          </DialogHeader>
          <form onSubmit={handleCreateSubmit} noValidate>
            <div className="space-y-3">
              <Field label={t("listeners.field_name")} required htmlFor="create-name" error={createTouched.name ? createErrors.name : undefined}>
                <Input id="create-name" value={createForm.name} onChange={createChange("name")} onBlur={createBlur("name")}
                  placeholder={t("listeners.name_ph")}
                  aria-invalid={!!(createTouched.name && createErrors.name)}
                  aria-describedby={createErrors.name ? "create-name-error" : undefined} />
              </Field>
              <Field label={t("listeners.field_type")} htmlFor="create-type">
                <Select value={createForm.type} onValueChange={typeChange(createSetField)}>
                  <SelectTrigger id="create-type" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="http">HTTP</SelectItem>
                    <SelectItem value="https">HTTPS</SelectItem>
                    <SelectItem value="tcp">TCP</SelectItem>
                    <SelectItem value="tls">TLS</SelectItem>
                    <SelectItem value="dns">DNS</SelectItem>
                    <SelectItem value="icmp">ICMP</SelectItem>
                    <SelectItem value="ssh">SSH</SelectItem>
                    <SelectItem value="h2c">H2C</SelectItem>
              <SelectItem value="udp">UDP</SelectItem>
              <SelectItem value="quic">QUIC</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t("listeners.field_host")} htmlFor="create-host" error={createTouched.host ? createErrors.host : undefined}>
                <Input id="create-host" value={createForm.host} onChange={createChange("host")} onBlur={createBlur("host")}
                  aria-invalid={!!(createTouched.host && createErrors.host)}
                  aria-describedby={createErrors.host ? "create-host-error" : undefined} />
              </Field>
              <Field label={t("listeners.field_port")} required htmlFor="create-port" error={createTouched.port ? createErrors.port : undefined}>
                <Input id="create-port" type="number" min="1" max="65535" value={createForm.port} onChange={createChange("port")} onBlur={createBlur("port")}
                  placeholder="8443"
                  aria-invalid={!!(createTouched.port && createErrors.port)}
                  aria-describedby={createErrors.port ? "create-port-error" : undefined} />
              </Field>
              {isTLSScheme(createForm.protocol || createForm.type) && tlsFields("create", createForm, createSetField)}
              <Field label={t("listeners.field_tags")} htmlFor="create-tags">
                <Input id="create-tags" value={createForm.tags} onChange={createChange("tags")}
                  placeholder={t("listeners.tags_ph")} />
              </Field>
              <Field label={t("listeners.field_color")} htmlFor="create-color">
                <Select value={createForm.color} onValueChange={(val) => createSetField("color", val ?? "")}>
                  <SelectTrigger id="create-color" className="w-full">
                    <SelectValue placeholder={t("listeners.color_none")} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="">{t("listeners.color_none")}</SelectItem>
                    <SelectItem value="red">{t("listeners.color_red")}</SelectItem>
                    <SelectItem value="orange">{t("listeners.color_orange")}</SelectItem>
                    <SelectItem value="yellow">{t("listeners.color_yellow")}</SelectItem>
                    <SelectItem value="green">{t("listeners.color_green")}</SelectItem>
                    <SelectItem value="blue">{t("listeners.color_blue")}</SelectItem>
                    <SelectItem value="purple">Purple</SelectItem>
                    <SelectItem value="pink">Pink</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
            </div>
            <DialogFooter>
              <Button variant="outline" type="button" onClick={() => setShowCreate(false)} className="flex-1">{t("listeners.cancel")}</Button>
              <Button type="submit" disabled={creating || createSubmitting || !createValid}
                className="flex-1">
                {creating ? t("listeners.creating") : t("listeners.create_listener")}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={showEdit} onOpenChange={(open) => { setShowEdit(open); if (!open) setEditingListener(null); }}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t("listeners.edit_dialog_title")}</DialogTitle>
          </DialogHeader>
          <form onSubmit={handleEditSubmit} noValidate>
            <div className="space-y-3">
              <Field label={t("listeners.field_name")} htmlFor="edit-name" error={editTouched.name ? editErrors.name : undefined}>
                <Input id="edit-name" value={editForm.name} onChange={handleEditChange("name")} onBlur={handleEditBlur("name")}
                  aria-invalid={!!(editTouched.name && editErrors.name)}
                  aria-describedby={editErrors.name ? "edit-name-error" : undefined} />
              </Field>
              <Field label={t("listeners.field_type")} htmlFor="edit-type">
                <Select value={editForm.type} onValueChange={typeChange(setEditChange)}>
                  <SelectTrigger id="edit-type" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="http">HTTP</SelectItem>
                    <SelectItem value="https">HTTPS</SelectItem>
                    <SelectItem value="tcp">TCP</SelectItem>
                    <SelectItem value="tls">TLS</SelectItem>
                    <SelectItem value="dns">DNS</SelectItem>
                    <SelectItem value="icmp">ICMP</SelectItem>
                    <SelectItem value="ssh">SSH</SelectItem>
                    <SelectItem value="h2c">H2C</SelectItem>
              <SelectItem value="udp">UDP</SelectItem>
              <SelectItem value="quic">QUIC</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
              <Field label={t("listeners.field_host")} htmlFor="edit-host" error={editTouched.host ? editErrors.host : undefined}>
                <Input id="edit-host" value={editForm.host} onChange={handleEditChange("host")} onBlur={handleEditBlur("host")}
                  aria-invalid={!!(editTouched.host && editErrors.host)}
                  aria-describedby={editErrors.host ? "edit-host-error" : undefined} />
              </Field>
              <Field label={t("listeners.field_port")} htmlFor="edit-port" error={editTouched.port ? editErrors.port : undefined}>
                <Input id="edit-port" type="number" min="1" max="65535" value={editForm.port} onChange={handleEditChange("port")} onBlur={handleEditBlur("port")}
                  aria-invalid={!!(editTouched.port && editErrors.port)}
                  aria-describedby={editErrors.port ? "edit-port-error" : undefined} />
              </Field>
              {isTLSScheme(editForm.protocol || editForm.type) && tlsFields("edit", editForm, setEditChange)}
              <Field label={t("listeners.field_notes")} htmlFor="edit-notes">
                <Input id="edit-notes" value={editForm.notes} onChange={handleEditChange("notes")} />
              </Field>
              <Field label={t("listeners.field_tags")} htmlFor="edit-tags">
                <Input id="edit-tags" value={editForm.tags} onChange={handleEditChange("tags")}
                  placeholder={t("listeners.tags_ph")} />
              </Field>
              <Field label={t("listeners.field_color")} htmlFor="edit-color">
                <Select value={editForm.color} onValueChange={(val) => setEditChange("color", val ?? "")}>
                  <SelectTrigger id="edit-color" className="w-full">
                    <SelectValue placeholder={t("listeners.color_none")} />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="">{t("listeners.color_none")}</SelectItem>
                    <SelectItem value="red">{t("listeners.color_red")}</SelectItem>
                    <SelectItem value="orange">{t("listeners.color_orange")}</SelectItem>
                    <SelectItem value="yellow">{t("listeners.color_yellow")}</SelectItem>
                    <SelectItem value="green">{t("listeners.color_green")}</SelectItem>
                    <SelectItem value="blue">{t("listeners.color_blue")}</SelectItem>
                    <SelectItem value="purple">Purple</SelectItem>
                    <SelectItem value="pink">Pink</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
            </div>
            <DialogFooter>
              <Button variant="outline" type="button" onClick={() => { setShowEdit(false); setEditingListener(null); }} className="flex-1">{t("listeners.cancel")}</Button>
              <Button type="submit" disabled={editSubmitting || !editValid} className="flex-1">{t("listeners.save")}</Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
      {modal}
    </PageContainer>
  );
}

