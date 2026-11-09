
import { registerFragment } from "@/lib/i18n";
import { enCred } from "@/lib/i18n/fragments/en.cred";
registerFragment(enCred);

import { toast } from "sonner";
import { useState, useMemo, useCallback, useEffect } from "react";
import { z } from "zod";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { downloadText } from "@/lib/download";
import { useI18n } from "@/lib/i18n";
import { useForm } from "@/lib/hooks/useForm";
import { ConfirmModal } from "@/components/ui/confirm-modal";
import { Field, fieldErrorId } from "@/components/ui/field";
import { PageContainer } from "@/components/ui/page-container";
import { PageToolbar } from "@/components/ui/page-toolbar";
import { CardHeaderRow } from "@/components/ui/card-header-row";
import { Pagination } from "@/components/ui/pagination";
import { DataState } from "@/components/ui/data-state";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Banner } from "@/components/ui/banner";
import { Input } from "@/components/ui/input";
import { SearchInput } from "@/components/SearchInput";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog";
import { Badge } from "@/components/ui/badge";
import { StatTile } from "@/components/ui/stat-tile";
import { Checkbox } from "@/components/ui/checkbox";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Label } from "@/components/ui/label";
import { Download, Lock, Plus, RefreshCw, Tag, ShieldCheck, AlertTriangle } from "lucide-react";
import { CRED_TYPES, TYPE_BADGE_VARIANT, type VaultEntry } from "./components/types";
import { useCredentialsData } from "./components/useCredentialsData";
import { CredentialRow } from "./components/CredentialRow";
import { csvCell } from "@/lib/csv";
import { CredHarvestCard } from "./components/CredHarvestCard";
import { CookieProxyCard } from "./components/CookieProxyCard";
import { useFilteredCredentials } from "./components/useFilteredCredentials";

const PAGE_SIZE = 20;

export default function CredentialsPage() {
  const { t } = useI18n();
  const { data, loading, error, reload, loadData } = useCredentialsData();
  const [searchQuery, setSearchQuery] = useState("");
  const [typeFilter, setTypeFilter] = useState("all");
  const [confirmedFilter, setConfirmedFilter] = useState("all");
  const [lifecycleFilter, setLifecycleFilter] = useState("all");
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [showAddModal, setShowAddModal] = useState(false);
  const [showEditModal, setShowEditModal] = useState(false);
  const [showBatchModal, setShowBatchModal] = useState(false);
  const [showDeleteConfirm, setShowDeleteConfirm] = useState<string | null>(null);
  const [editTarget, setEditTarget] = useState<VaultEntry | null>(null);
  const [savingEdit, setSavingEdit] = useState(false);
  const [page, setPage] = useState(1);

  const [showPasswords, setShowPasswords] = useState<Set<string>>(new Set());
  const [showHashes, setShowHashes] = useState<Set<string>>(new Set());

  type CredFormValues = {
    domain: string;
    username: string;
    password: string;
    hash: string;
    type: string;
    source: string;
    tags: string;
    notes: string;
  };

  const emptyCredForm = (): CredFormValues => ({
    domain: "",
    username: "",
    password: "",
    hash: "",
    type: "cleartext",
    source: "",
    tags: "",
    notes: "",
  });

  const credSchema = useMemo(
    () =>
      z.object({
        domain: z.string(),
        username: z.string().trim().min(1, t("cred.toast.username_required")),
        password: z.string(),
        hash: z.string(),
        type: z.string().min(1),
        source: z.string(),
        tags: z.string(),
        notes: z.string(),
      }),
    [t],
  );

  const {
    values: form,
    errors: formErrors,
    touched: formTouched,
    isSubmitting: formSubmitting,
    isValid: formValid,
    handleChange: formChange,
    handleBlur: formBlur,
    setFieldValue: formSetField,
    handleSubmit: formSubmit,
    resetForm,
  } = useForm<CredFormValues>({
    initialValues: emptyCredForm(),
    schema: credSchema,
    onSubmit: async (vals) => {
      try {
        await api.post(paths.credentials.add, {
          domain: vals.domain,
          username: vals.username,
          password: vals.password,
          hash: vals.hash,
          type: vals.type,
          source: vals.source || "manual",
          tags: vals.tags,
          notes: vals.notes,
        });
        showToastNotify(t("cred.toast.added"), "success");
        setShowAddModal(false);
        resetForm();
        loadData();
      } catch {
        showToastNotify(t("common.error"), "error");
      }
    },
  });

  const [batchTags, setBatchTags] = useState("");

  const showToastNotify = (text: string, type: "success" | "error" | "info" = "info") => {
    if (type === "success") toast.success(text);
    else if (type === "error") toast.error(text);
    else toast.info(text);
  };

  const handleEdit = async () => {
    if (!editTarget) return;
    try {
      setSavingEdit(true);
      const body: Record<string, string> = {};
      body.tags = form.tags || "";
      body.notes = form.notes || "";
      await api.put(paths.credentials.one(editTarget.id), body);
      showToastNotify(t("cred.toast.updated"), "success");
      setShowEditModal(false);
      setEditTarget(null);
      resetForm();
      loadData();
    } catch {
      showToastNotify(t("common.error"), "error");
    } finally {
      setSavingEdit(false);
    }
  };

  const requestDelete = useCallback((id: string) => setShowDeleteConfirm(id), []);

  const handleDelete = useCallback(async (id: string) => {
    try {
      await api.del(paths.credentials.one(id));
      showToastNotify(t("cred.toast.deleted"), "success");
      setShowDeleteConfirm(null);
      loadData();
    } catch {
      showToastNotify(t("common.error"), "error");
    }
  }, [t, loadData]);

  const handleToggleConfirm = useCallback(async (entry: VaultEntry) => {
    try {
      await api.post(paths.credentials.confirm(entry.id));
      loadData();
    } catch { toast.error(t("cred.toast.confirm_failed")); }
  }, [loadData, t]);

  const handleVerify = useCallback(async (entry: VaultEntry) => {
    if (!entry.password || !entry.agent_id) return;
    try {
      await api.post(paths.agents.cmd(entry.agent_id, "cred_check"), {
        user: entry.username,
        domain: entry.domain || "",
        password: entry.password,
      });
      showToastNotify(t("cred.toast.verify_success"), "success");
      loadData();
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      if (msg.includes("fuse_tripped")) {
        showToastNotify(t("cred.toast.verify_fuse"), "error");
      } else if (msg.includes("403") || msg.includes("Forbidden")) {
        showToastNotify(t("cred.toast.verify_forbidden"), "error");
      } else {
        showToastNotify(t("cred.toast.verify_failed"), "error");
      }
    }
  }, [loadData, t]);

  const handleMarkUsed = useCallback(async (id: string) => {
    try {
      await api.post(paths.credentials.usage(id), { action: "manual", detail: "operator marked used" });
      showToastNotify(t("cred.toast.used"), "success");
      loadData();
    } catch {
      showToastNotify(t("cred.toast.verify_failed"), "error");
    }
  }, [loadData, t]);

  const handleBatchTags = async () => {
    if (!batchTags || selectedIds.size === 0) return;
    try {
      await api.postJson(paths.credentials.batchTags, {
        ids: Array.from(selectedIds).map((id) => Number(id)),
        tags: batchTags.split(",").map((tag) => tag.trim()).filter(Boolean),
      });
      showToastNotify(t("cred.toast.tags_added", { count: selectedIds.size }), "success");
      setShowBatchModal(false);
      setBatchTags("");
      setSelectedIds(new Set());
      loadData();
    } catch {
      showToastNotify(t("common.error"), "error");
    }
  };

  const exportCSV = () => {
    const headers = ["Type", "Domain", "Username", "Password", "Hash", "Source", "Tags", "Confirmed", "Notes"].map(csvCell);
    const rows = filteredEntries.map(e => [
      e.type,
      e.domain || "",
      e.username,
      e.password || "",
      e.hash || "",
      e.source || "",
      e.tags || "",
      e.confirmed ? "Yes" : "No",
      e.notes || "",
    ].map(csvCell));
    const csv = [headers, ...rows].map(r => r.join(",")).join("\n");
    downloadText(csv, `credentials_${new Date().toISOString().slice(0, 10)}.csv`, "text/csv");
    showToastNotify(t("cred.toast.csv_exported"), "success");
  };

  const openEdit = useCallback((entry: VaultEntry) => {
    setEditTarget(entry);
    resetForm({
      domain: entry.domain || "",
      username: entry.username || "",
      password: entry.password || "",
      hash: entry.hash || "",
      type: entry.type || "cleartext",
      source: entry.source || "",
      tags: entry.tags || "",
      notes: entry.notes || "",
    });
    setShowEditModal(true);
  }, [resetForm]);

  const toggleSelect = useCallback((id: string) => {
    setSelectedIds(prev => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  }, []);

  const [verifying, setVerifying] = useState(false);

  // Batch-verify selected credentials: queues one cred_check task per entry
  // on its harvesting agent; results flip verify_status via the beacon path.
  const handleBatchVerify = async () => {
    if (selectedIds.size === 0 || verifying) return;
    setVerifying(true);
    try {
      const d = await api.postJson<{ queued: number; results: Array<{ status: string }> }>(
        paths.credentials.batchVerify,
        { ids: [...selectedIds] },
      );
      toast.success(t("cred.toast.verify_queued", { queued: String(d.queued) }));
      const skipped = d.results.filter(r => r.status.startsWith("skipped")).length;
      if (skipped > 0) {
        toast.info(t("cred.toast.verify_skipped", { skipped: String(skipped) }));
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t("cred.toast.verify_failed"));
    } finally {
      setVerifying(false);
    }
  };

  const toggleSelectAll = () => {
    // Decide from the CURRENT view, not set size: a selection spanning other
    // filters/pages could make size match while none of this view is picked,
    // inverting the expected toggle.
    const allSelected = filteredEntries.length > 0 && filteredEntries.every(e => selectedIds.has(String(e.id)));
    if (allSelected) {
      setSelectedIds(prev => {
        const currentView = new Set(filteredEntries.map(e => String(e.id)));
        const next = new Set([...prev].filter(id => !currentView.has(id)));
        return next;
      });
    } else {
      setSelectedIds(prev => {
        const next = new Set(prev);
        for (const e of filteredEntries) next.add(String(e.id));
        return next;
      });
    }
  };

  const togglePasswordVisibility = useCallback((id: string) => {
    setShowPasswords(prev => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  }, []);

  const toggleHashVisibility = useCallback((id: string) => {
    setShowHashes(prev => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  }, []);

  const entries = useMemo(() => data?.VaultEntries || [], [data?.VaultEntries]);

  // Reconcile selection with reality: deleted/refreshed-away entries must not
  // linger as ghosts that inflate the "N selected" count and get swept into
  // batch operations.
  useEffect(() => {
    setSelectedIds(prev => {
      if (prev.size === 0) return prev;
      const valid = new Set(entries.map(e => String(e.id)));
      const next = new Set([...prev].filter(id => valid.has(id)));
      return next.size === prev.size ? prev : next;
    });
  }, [entries]);

  const filteredEntries = useFilteredCredentials(
    entries,
    searchQuery,
    typeFilter,
    confirmedFilter,
    lifecycleFilter,
  );

  const pageCount = Math.max(1, Math.ceil(filteredEntries.length / PAGE_SIZE));
  const currentPage = Math.min(page, pageCount);
  const paginatedEntries = filteredEntries.slice(
    (currentPage - 1) * PAGE_SIZE,
    currentPage * PAGE_SIZE,
  );

  const handleSearchChange = (value: string) => { setSearchQuery(value); setPage(1); };
  const handleTypeFilterChange = (value: string | null) => { setTypeFilter(value ?? ""); setPage(1); };
  const handleConfirmedFilterChange = (value: string | null) => { setConfirmedFilter(value ?? "all"); setPage(1); };
  const handleLifecycleFilterChange = (value: string | null) => { setLifecycleFilter(value ?? "all"); setPage(1); };
  const handleClearFilters = () => { setSearchQuery(""); setTypeFilter("all"); setConfirmedFilter("all"); setLifecycleFilter("all"); setPage(1); };

  const stats = useMemo(() => ({
    total: data?.VaultCount || 0,
    confirmed: entries.filter(e => e.confirmed).length || 0,
    unconfirmed: entries.filter(e => !e.confirmed).length || 0,
    byType: CRED_TYPES.slice(1).map(ct => ({
      type: ct,
      count: entries.filter(e => e.type === ct).length || 0,
    })),
  }), [entries, data?.VaultCount]);

  return (
    <>
      <PageContainer
        title={t("cred.title")}
        subtitle={t("cred.subtitle")}
        actions={
          <>
            {filteredEntries.length > 0 && (
              <Button
                onClick={exportCSV}
                size="lg"
                className="gap-x-2"
              >
                <Download className="size-4" />
                <span>{t("cred.export_csv")}</span>
              </Button>
            )}
            <Button
              onClick={() => { resetForm(); setShowAddModal(true); }}
              size="lg"
              className="gap-x-2"
            >
              <Plus className="size-4" />
              <span>{t("cred.add")}</span>
            </Button>
          </>
        }
      >

      <Banner tone="warning" className="items-start">
        <div className="font-semibold">{t("cred.honesty_title")}</div>
        <div className="text-xs text-muted-foreground mt-0.5">{t("cred.honesty_desc")}</div>
      </Banner>

      <CredHarvestCard />
      <CookieProxyCard />

      <div className="grid grid-cols-2 sm:grid-cols-4 gap-4 sm:gap-5">
        <Card interactive className="p-(--card-spacing)">
          <StatTile label={t("cred.stat_total")} value={loading ? "…" : stats.total} icon={<Lock className="size-5" />} />
        </Card>
        <Card interactive className="p-(--card-spacing)">
          <StatTile label={t("cred.stat_confirmed")} value={loading ? "…" : stats.confirmed} tone="success" icon={<ShieldCheck className="size-5" />} />
        </Card>
        <Card interactive className="p-(--card-spacing)">
          <StatTile label={t("cred.stat_unconfirmed")} value={loading ? "…" : stats.unconfirmed} tone="warning" icon={<AlertTriangle className="size-5" />} />
        </Card>
        <Card interactive className="p-(--card-spacing)">
          <div className="flex items-center justify-between gap-3">
            <div>
              <div className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">{t("cred.stat_by_type")}</div>
              <div className="flex flex-wrap gap-1 mt-2">
                {stats.byType.map(s => s.count > 0 && (
                  <Badge key={s.type} variant={TYPE_BADGE_VARIANT[s.type] || "outline"} className="text-micro-sm">{s.type}: {s.count}</Badge>
                ))}
                {stats.byType.every(s => s.count === 0) && <span className="text-xs text-muted-foreground">—</span>}
              </div>
            </div>
            <span className="shrink-0 rounded-xl p-2.5 bg-primary/10 text-primary ring-1 ring-border/50"><Tag className="size-5" /></span>
          </div>
        </Card>
      </div>

      <PageToolbar
        sticky
        primary={selectedIds.size > 0 ? (
          <>
            <Button onClick={() => setShowBatchModal(true)} className="gap-x-2">
              <Tag className="size-4" />
              <span>{t("cred.batch_tags")} ({selectedIds.size})</span>
            </Button>
            <Button
              onClick={handleBatchVerify}
              variant="secondary"
              disabled={verifying}
              className="gap-x-2"
            >
              <ShieldCheck className={`size-4 ${verifying ? "animate-pulse" : ""}`} />
              <span>{verifying ? t("cred.verifying") : `${t("cred.batch_verify")} (${selectedIds.size})`}</span>
            </Button>
          </>
        ) : undefined}
      >
        <SearchInput
          value={searchQuery}
          onChange={handleSearchChange}
          onClear={() => { setSearchQuery(""); setPage(1); }}
          placeholder={t("cred.search_placeholder")}
          className="w-full sm:max-w-xs"
          label={t("common.search")}
        />
        <Select value={typeFilter} onValueChange={handleTypeFilterChange}>
          <SelectTrigger className="w-full sm:w-[140px]" aria-label={t("cred.col_type")}>
            <SelectValue>
              {(value) => (!value || value === "all")
                ? t("cred.filter_all_types")
                : String(value).charAt(0).toUpperCase() + String(value).slice(1)}
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            {CRED_TYPES.map(ct => (
              <SelectItem key={ct} value={ct}>{ct === "all" ? t("cred.filter_all_types") : ct.charAt(0).toUpperCase() + ct.slice(1)}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={confirmedFilter} onValueChange={handleConfirmedFilterChange}>
          <SelectTrigger className="w-full sm:w-[140px]" aria-label={t("cred.col_confirmed")}>
            <SelectValue>
              {(value) => value === "true"
                ? t("cred.filter_confirmed")
                : value === "false"
                  ? t("cred.filter_unconfirmed")
                  : t("cred.filter_all_status")}
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("cred.filter_all_status")}</SelectItem>
            <SelectItem value="true">{t("cred.filter_confirmed")}</SelectItem>
            <SelectItem value="false">{t("cred.filter_unconfirmed")}</SelectItem>
          </SelectContent>
        </Select>
        <Select value={lifecycleFilter} onValueChange={handleLifecycleFilterChange}>
          <SelectTrigger className="w-full sm:w-[150px]" aria-label={t("cred.filter_all_lifecycle")}>
            <SelectValue>
              {(value) => {
                switch (value) {
                  case "fresh": return t("cred.lifecycle_fresh");
                  case "verified": return t("cred.lifecycle_verified");
                  case "used": return t("cred.lifecycle_used");
                  case "stale": return t("cred.lifecycle_stale");
                  default: return t("cred.filter_all_lifecycle");
                }
              }}
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("cred.filter_all_lifecycle")}</SelectItem>
            <SelectItem value="fresh">{t("cred.lifecycle_fresh")}</SelectItem>
            <SelectItem value="verified">{t("cred.lifecycle_verified")}</SelectItem>
            <SelectItem value="used">{t("cred.lifecycle_used")}</SelectItem>
            <SelectItem value="stale">{t("cred.lifecycle_stale")}</SelectItem>
          </SelectContent>
        </Select>
        <Button
          variant="ghost"
          size="icon"
          onClick={() => reload()}
          aria-label={t("common.refresh")}
          title={t("common.refresh")}
        >
          <RefreshCw className="size-4" />
        </Button>
        {(searchQuery || typeFilter !== "all" || confirmedFilter !== "all" || lifecycleFilter !== "all") && (
          <Button variant="ghost" onClick={handleClearFilters}>
            {t("cred.clear")}
          </Button>
        )}
      </PageToolbar>

      {data?.AllTags && data.AllTags.length > 0 && (
        <Card className="p-(--card-spacing)">
          <div className="font-medium text-sm text-foreground flex items-center gap-2 mb-3">
            <Tag className="size-4" />
            <span>{t("cred.tags")}</span>
          </div>
          <div className="flex flex-wrap gap-2">
            {data.AllTags.map(tag => (
              <Badge
                key={tag}
                variant="outline"
                className="cursor-pointer"
                onClick={() => { setSearchQuery(tag); setPage(1); }}
              >
                {tag}
              </Badge>
            ))}
          </div>
        </Card>
      )}

      <Card className="overflow-hidden shadow-sm hover:shadow-md transition-shadow duration-200">
        <CardHeaderRow accent={false} icon={Lock} tone="primary" title={t("cred.vault_title")} action={filteredEntries.length > 0 ? <Badge variant="outline" className="font-mono">{filteredEntries.length}</Badge> : undefined} />

        <DataState
          loading={loading}
          error={error}
          empty={!loading && !error && filteredEntries.length === 0}
          emptyIcon={Lock}
          emptyTitle={t("cred.empty")}
          onRetry={reload}
          loadingSkeleton={
            <div className="p-(--card-spacing) text-center text-muted-foreground">
              <div className="flex flex-col items-center gap-2">
                <Skeleton className="size-8 rounded-full" />
                <Skeleton className="h-4 w-20" />
              </div>
            </div>
          }
        >
          <div role="region" aria-label={t("cred.vault_title")} tabIndex={0} className="content-auto overscroll-contain overflow-x-auto scrollbar-thin">
            <Table className="w-full text-sm">
              <TableHeader className="bg-card/95 backdrop-blur supports-[backdrop-filter]:bg-card/90 sticky top-0 z-10 border-b border-border">
                <TableRow className="text-xs text-muted-foreground hover:bg-transparent">
                  <TableHead className="text-left py-3 px-2 font-normal">
                    <Checkbox
                      checked={selectedIds.size === filteredEntries.length && filteredEntries.length > 0}
                      onCheckedChange={() => toggleSelectAll()}
                    />
                  </TableHead>                  <TableHead className="text-left py-3 px-4 font-normal">{t("cred.col_type")}</TableHead>
                  <TableHead className="text-left py-3 px-4 font-normal">{t("cred.col_username")}</TableHead>
                  <TableHead className="text-left py-3 px-4 font-normal">{t("cred.col_password")}</TableHead>
                  <TableHead className="max-sm:hidden text-left py-3 px-4 font-normal">{t("cred.col_hash")}</TableHead>
                  <TableHead className="max-sm:hidden text-left py-3 px-4 font-normal">{t("cred.col_domain")}</TableHead>
                  <TableHead className="max-sm:hidden text-left py-3 px-4 font-normal">{t("cred.col_source")}</TableHead>
                  <TableHead className="max-sm:hidden text-left py-3 px-4 font-normal">{t("cred.col_confirmed")}</TableHead>
                  <TableHead className="max-sm:hidden text-left py-3 px-4 font-normal">{t("cred.col_tags")}</TableHead>
                  <TableHead className="text-center py-3 px-4 font-normal">{t("cred.col_actions")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody className="divide-y divide-border">
                {paginatedEntries.map(entry => (
                  <CredentialRow
                    key={entry.id}
                    entry={entry}
                    isSelected={selectedIds.has(entry.id)}
                    showPassword={showPasswords.has(entry.id)}
                    showHash={showHashes.has(entry.id)}
                    onToggleSelect={toggleSelect}
                    onToggleConfirm={handleToggleConfirm}
                    onEdit={openEdit}
                    onDelete={requestDelete}
                    onVerify={handleVerify}
                    onMarkUsed={handleMarkUsed}
                    togglePasswordVisibility={togglePasswordVisibility}
                    toggleHashVisibility={toggleHashVisibility}
                    t={t}
                  />
                ))}
              </TableBody>
            </Table>
          </div>
        </DataState>
        <Pagination page={currentPage} pageSize={PAGE_SIZE} total={filteredEntries.length} onPageChange={setPage} />
      </Card>

      <Dialog open={showAddModal} onOpenChange={setShowAddModal}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>{t("cred.add_title")}</DialogTitle>
          </DialogHeader>
          <form onSubmit={formSubmit} noValidate>
          <div className="space-y-4">
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Field label={t("cred.field_type")} required>
                <Select value={form.type} onValueChange={(v) => formSetField("type", v ?? "cleartext")}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {CRED_TYPES.filter(ct => ct !== "all").map(ct => (
                      <SelectItem key={ct} value={ct}>{ct.charAt(0).toUpperCase() + ct.slice(1)}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field
                label={t("cred.field_username")}
                required
                htmlFor="add-cred-username"
                error={formTouched.username ? formErrors.username : undefined}
              >
                <Input
                  id="add-cred-username"
                  value={form.username}
                  onChange={formChange("username")}
                  onBlur={formBlur("username")}
                  aria-invalid={!!(formTouched.username && formErrors.username)}
                  aria-describedby={formErrors.username ? fieldErrorId("add-cred-username") : undefined}
                />
              </Field>
              <Field label={t("cred.field_password")} htmlFor="add-cred-password">
                <Input
                  id="add-cred-password"
                  value={form.password}
                  onChange={formChange("password")}
                />
              </Field>
              <Field label={t("cred.field_domain")} htmlFor="add-cred-domain">
                <Input
                  id="add-cred-domain"
                  value={form.domain}
                  onChange={formChange("domain")}
                />
              </Field>
              <Field label={t("cred.field_source")} htmlFor="add-cred-source">
                <Input
                  id="add-cred-source"
                  value={form.source}
                  onChange={formChange("source")}
                  placeholder={t("cred.ph_source")}
                />
              </Field>
              <Field label={t("cred.field_hash")} htmlFor="add-cred-hash">
                <Input
                  id="add-cred-hash"
                  value={form.hash}
                  onChange={formChange("hash")}
                  className="font-mono text-xs"
                />
              </Field>
            </div>
            <Field label={t("cred.field_tags")} htmlFor="add-cred-tags">
              <Input
                id="add-cred-tags"
                value={form.tags}
                onChange={formChange("tags")}
                placeholder={t("cred.ph_tags")}
              />
            </Field>
            <Field label={t("cred.field_notes")} htmlFor="add-cred-notes">
              <Textarea
                id="add-cred-notes"
                value={form.notes}
                onChange={formChange("notes")}
                rows={2}
                className="resize-none"
              />
            </Field>
          </div>
          <DialogFooter>
            <Button variant="outline" type="button" onClick={() => setShowAddModal(false)} className="flex-1">
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={formSubmitting || !formValid} className="flex-1">
              {t("cred.btn.add")}
            </Button>
          </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={showEditModal} onOpenChange={(open) => { if (!open) { setShowEditModal(false); setEditTarget(null); } }}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>{t("cred.edit_title")}</DialogTitle>
          </DialogHeader>
          <p className="text-xs text-muted-foreground">{t("cred.edit_readonly_hint")}</p>
          <div className="space-y-4">
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Field label={t("cred.field_type")} htmlFor="edit-cred-type">
                <Input id="edit-cred-type" value={form.type} disabled />
              </Field>
              <Field label={t("cred.field_username")} htmlFor="edit-cred-username">
                <Input
                  id="edit-cred-username"
                  value={form.username}
                  disabled
                />
              </Field>
              <Field label={t("cred.field_password")} htmlFor="edit-cred-password">
                <Input
                  id="edit-cred-password"
                  value={form.password}
                  type="password"
                  disabled
                />
              </Field>
              <Field label={t("cred.field_domain")} htmlFor="edit-cred-domain">
                <Input
                  id="edit-cred-domain"
                  value={form.domain}
                  disabled
                />
              </Field>
              <Field label={t("cred.field_source")} htmlFor="edit-cred-source">
                <Input
                  id="edit-cred-source"
                  value={form.source}
                  disabled
                />
              </Field>
              <Field label={t("cred.field_hash")} htmlFor="edit-cred-hash">
                <Input
                  id="edit-cred-hash"
                  value={form.hash}
                  className="font-mono text-xs"
                  disabled
                />
              </Field>
            </div>
            <Field label={t("cred.field_tags")} htmlFor="edit-cred-tags">
              <Input
                id="edit-cred-tags"
                value={form.tags}
                onChange={formChange("tags")}
              />
            </Field>
            <Field label={t("cred.field_notes")} htmlFor="edit-cred-notes">
              <Textarea
                id="edit-cred-notes"
                value={form.notes}
                onChange={formChange("notes")}
                rows={2}
                className="resize-none"
              />
            </Field>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => { setShowEditModal(false); setEditTarget(null); }} className="flex-1">
              {t("common.cancel")}
            </Button>
            <Button onClick={handleEdit} disabled={savingEdit} className="flex-1">
              {t("common.save")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={showBatchModal} onOpenChange={setShowBatchModal}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t("cred.batch_title")}</DialogTitle>
          </DialogHeader>
          <p className="text-sm text-muted-foreground">
            {t("cred.batch_desc", { count: selectedIds.size })}
          </p>
          <div>
            <Label className="block mb-1.5">{t("cred.field_tags")}</Label>
            <Input
              value={batchTags}
              onChange={e => setBatchTags(e.target.value)}
              placeholder={t("credentials.tags_ph")}
            />
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowBatchModal(false)} className="flex-1">
              {t("common.cancel")}
            </Button>
            <Button onClick={handleBatchTags} className="flex-1">
              {t("cred.batch_btn")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmModal
        open={!!showDeleteConfirm}
        title={t("cred.delete_title")}
        message={t("cred.delete_message")}
        danger
        confirmText={t("common.delete")}
        onCancel={() => setShowDeleteConfirm(null)}
        onConfirm={() => { if (showDeleteConfirm) void handleDelete(showDeleteConfirm); }}
      />
    </PageContainer>
    </>
  );
}
