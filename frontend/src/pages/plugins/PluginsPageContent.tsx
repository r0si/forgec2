
import { registerFragment } from "@/lib/i18n";
import { enPlugins } from "@/lib/i18n/fragments/en.plugins";
registerFragment(enPlugins);

import { useState, useMemo } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { fetchAllAgents } from "@/lib/agents";
import { downloadBlob } from "@/lib/download";
import { useI18n } from "@/lib/i18n";
import { Pagination } from "@/components/ui/pagination";
import { Spinner } from "@/components/ui/spinner";
import { PageContainer } from "@/components/ui/page-container";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { DataState } from "@/components/ui/data-state";
import { PageSkeleton } from "@/components/ui/page-skeleton";
import { toast } from "sonner";
import { Card, CardContent } from "@/components/ui/card";
import { PageToolbar } from "@/components/ui/page-toolbar";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Textarea } from "@/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from "@/components/ui/dialog";
import { CheckCircle, CloudUpload, FileDown, LayoutGrid, List, Play, Plus, Puzzle } from "lucide-react";
import { SearchInput } from "@/components/SearchInput";
import { PLUGIN_CATEGORIES } from "./components/categories";
import { PluginCard, PluginListItem, PluginDetailModal, ReviewsModal } from "./components/PluginUI";
import type { Plugin, Review } from "./components/types";
import { usePluginsData } from "./components/usePluginsData";

const PAGE_SIZE = 12;

export default function PluginsPage() {
  const { t } = useI18n();
  const { plugins, loading, error, loadPlugins } = usePluginsData();
  const [search, setSearch] = useState("");
  const [category, setCategory] = useState("");
  const [detailPlugin, setDetailPlugin] = useState<Plugin | null>(null);
  const [actionStates, setActionStates] = useState<Record<string, string>>({});
  const [viewMode, setViewMode] = useState<"grid" | "list">("grid");
  const [showCreate, setShowCreate] = useState(false);
  const [showImport, setShowImport] = useState(false);
  const [showExecute, setShowExecute] = useState(false);
  const [executePlugin, setExecutePlugin] = useState<Plugin | null>(null);
  const [executeResult, setExecuteResult] = useState<string | null>(null);
  const [showReviews, setShowReviews] = useState(false);
  const [reviewsPlugin, setReviewsPlugin] = useState<Plugin | null>(null);
  const [reviews, setReviews] = useState<Review[]>([]);
  const [createForm, setCreateForm] = useState({ name: "", description: "", author: "", category: "", version: "1.0.0" });
  const [importFile, setImportFile] = useState<File | null>(null);
  const [page, setPage] = useState(1);
  const { confirm, modal } = useConfirm();

  const filtered = useMemo(() => plugins.filter((p) => {
    const name = (p.name || "").toLowerCase();
    const desc = (p.description || "").toLowerCase();
    const author = (p.author || "").toLowerCase();
    const term = search.toLowerCase();
    const matchSearch = name.includes(term) || desc.includes(term) || author.includes(term);
    const matchCategory = !category || (p.category || "") === category;
    return matchSearch && matchCategory;
  }), [plugins, search, category]);

  const pageCount = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
  const currentPage = Math.min(page, pageCount);
  const paginatedFiltered = filtered.slice((currentPage - 1) * PAGE_SIZE, currentPage * PAGE_SIZE);

  const handleSearchChange = (value: string) => { setSearch(value); setPage(1); };
  const handleCategoryChange = (value: string) => { setCategory(value); setPage(1); };

  const setAction = (id: string, action: string | null) => {
    setActionStates((s) => {
      const n = { ...s };
      if (action === null) delete n[id]; else n[id] = action;
      return n;
    });
  };

  const handleInstall = async (pluginId: string) => {
    setAction(pluginId, "installing");
    try {
      await api.post(paths.plugins.install(pluginId), {});
      toast.success(t("plugins.toast.installed"));
      loadPlugins();
    } catch { toast.error(t("plugins.toast.install_failed")); }
    finally { setAction(pluginId, null); }
  };

  const handleUninstall = async (pluginId: string) => {
    setAction(pluginId, "uninstalling");
    try {
      await api.postJson(paths.plugins.toggle(pluginId), { enabled: false });
      toast.success(t("plugins.toast.uninstalled"));
      loadPlugins();
    } catch { toast.error(t("plugins.toast.uninstall_failed")); }
    finally { setAction(pluginId, null); }
  };

  const handleDelete = async (pluginId: string) => {
    if (!(await confirm({ message: t("plugins.confirm_delete_msg") }))) return;
    setAction(pluginId, "deleting");
    try {
      await api.del(paths.plugins.one(pluginId));
      toast.success(t("plugins.toast.deleted"));
      loadPlugins();
    } catch { toast.error(t("plugins.toast.delete_failed")); }
    finally { setAction(pluginId, null); }
  };

  const handleToggle = async (pluginId: string, enabled: boolean) => {
    setAction(pluginId, "toggling");
    try {
      await api.postJson(paths.plugins.toggle(pluginId), { enabled });
      toast.success(enabled ? t("plugins.toast.enabled") : t("plugins.toast.disabled"));
      loadPlugins();
    } catch { toast.error(t("plugins.toast.toggle_failed")); }
    finally { setAction(pluginId, null); }
  };

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      const body: Record<string, string> = {};
      Object.entries(createForm).forEach(([k, v]) => { if (v) body[k] = v; });
      await api.postJson(paths.plugins.create, body);
      toast.success(t("plugins.toast.created"));
      setShowCreate(false);
      setCreateForm({ name: "", description: "", author: "", category: "", version: "1.0.0" });
      loadPlugins();
    } catch { toast.error(t("plugins.toast.create_failed")); }
  };

  const [executeLoading, setExecuteLoading] = useState(false);
  const [executeAgentId, setExecuteAgentId] = useState("");
  const [executeAgents, setExecuteAgents] = useState<{ id: string; hostname?: string }[]>([]);

  const loadExecuteAgents = async () => {
    try {
      // Full-fleet fetch: a single page-size request would be truncated to
      // the server's 100-row cap on large fleets.
      const list = await fetchAllAgents();
      setExecuteAgents(list.map((a) => ({ id: String(a.id ?? ""), hostname: a.hostname })));
    } catch { setExecuteAgents([]); }
  };

  const handleExecute = async (pluginId: string) => {
    setExecuteResult(null);
    setExecuteLoading(true);
    try {
      const result = await api.postJson(paths.plugins.execute(pluginId), {
        agent_id: executeAgentId || undefined,
        params: {},
      });
      setExecuteResult(JSON.stringify(result, null, 2));
      toast.success(t("plugins.toast.executed"));
    } catch {
      setExecuteResult(t("plugins.execution_failed"));
      toast.error(t("plugins.toast.execute_failed"));
    } finally {
      setExecuteLoading(false);
    }
  };

  const handleExport = async (pluginId: string) => {
    try {
      const { blob, filename } = await api.downloadGet(paths.plugins.export(pluginId), `plugin_${pluginId}.json`);
      downloadBlob(blob, filename);
      toast.success(t("plugins.toast.exported"));
    } catch { toast.error(t("plugins.toast.export_failed")); }
  };

  const handleImport = async () => {
    if (!importFile) return;
    try {
      const formData = new FormData();
      formData.append("file", importFile);
      await api.postFormData(paths.plugins.importJson, formData);
      toast.success(t("plugins.toast.imported"));
      setShowImport(false);
      setImportFile(null);
      loadPlugins();
    } catch { toast.error(t("plugins.toast.import_failed")); }
  };

  const handleUpdate = async (pluginId: string) => {
    setAction(pluginId, "updating");
    try {
      await api.post(paths.plugins.update(pluginId));
      toast.success(t("plugins.toast.updated"));
      loadPlugins();
    } catch { toast.error(t("plugins.toast.update_failed")); }
    finally { setAction(pluginId, null); }
  };

  const handleUpdateCheck = async () => {
    try {
      await api.post(paths.plugins.checkUpdates);
      toast.success(t("plugins.toast.update_check_done"));
      loadPlugins();
    } catch { toast.error(t("plugins.toast.update_check_failed")); }
  };

  const handleLoadReviews = async (plugin: Plugin) => {
    const pid = plugin.id || "";
    setReviewsPlugin(plugin);
    setShowReviews(true);
    try {
      const apiData = await api.get<{ reviews?: Review[]; data?: Review[] } | Review[]>(paths.plugins.reviews(pid));
      const d = apiData as { reviews?: Review[]; data?: Review[] };
      setReviews(d.data || d.reviews || (Array.isArray(apiData) ? apiData : []));
    } catch {
      setReviews([]);
      toast.error(t("plugins.toast.reviews_load_failed"));
    }
  };

  const handlePostReview = async (pluginId: string, rating: number, content: string) => {
    try {
      // Backend binds JSON {rating:int, comment:string} via ShouldBindJSON.
      await api.postJson(paths.plugins.reviews(pluginId), { rating, comment: content });
      toast.success(t("plugins.toast.review_posted"));
      if (reviewsPlugin) handleLoadReviews(reviewsPlugin);
    } catch { toast.error(t("plugins.toast.review_post_failed")); }
  };

  const handleRating = async (pluginId: string, rating: number) => {
    try {
      await api.postJson(paths.plugins.rating(pluginId), { rating });
      toast.success(t("plugins.toast.rating_submitted"));
      loadPlugins();
    } catch { toast.error(t("plugins.toast.rating_submit_failed")); }
  };

  const handleLoadDependencies = async (plugin: Plugin) => {
    const pid = plugin.id || "";
    try {
      const apiData = await api.get<{ dependencies?: string[]; data?: string[] } | string[]>(paths.plugins.dependencies(pid));
      const d = apiData as { dependencies?: string[]; data?: string[] };
      const deps = d.data || d.dependencies || (Array.isArray(apiData) ? apiData : []);
      setDetailPlugin({ ...plugin, Dependencies: Array.isArray(deps) ? deps : [] });
    } catch { toast.error(t("plugins.toast.load_deps_failed")); }
  };

  return (
    <PageContainer title={t("plugins.title")} icon={<Puzzle className="size-4" />} subtitle={t("plugins.subtitle", { count: String(plugins.length) })} actions={<>
        <div className="flex items-center gap-2 flex-wrap">
          <Button variant="outline" size="lg" onClick={handleUpdateCheck}>
            <CheckCircle className="size-4" /> {t("plugins.check_updates")}
          </Button>
          <Button onClick={() => setShowCreate(true)}>
            <Plus className="size-4" /> {t("plugins.create")}
          </Button>
          <Button variant="secondary" onClick={() => setShowImport(true)}>
            <FileDown className="size-4" /> {t("plugins.import")}
          </Button>
          <div className="flex bg-secondary rounded-lg p-0.5">
            <Button variant={viewMode === "grid" ? "secondary" : "ghost"} size="icon-sm" onClick={() => setViewMode("grid")} aria-label={t("plugins.grid_view")}>
              <LayoutGrid className="size-4" />
            </Button>
            <Button variant={viewMode === "list" ? "secondary" : "ghost"} size="icon-sm" onClick={() => setViewMode("list")} aria-label={t("plugins.list_view")}>
              <List className="size-4" />
            </Button>
          </div>
        </div>
      </>}>

      <PageToolbar sticky>
          <SearchInput value={search} onChange={handleSearchChange} placeholder={t("plugins.search_ph")} className="max-w-md" label={t("common.search")} />
      </PageToolbar>

      <div className="flex flex-wrap gap-2 mb-4">
        {PLUGIN_CATEGORIES.map((cat) => {
          const count = cat.key === "" ? plugins.length : plugins.filter((p) => (p.category || "") === cat.key).length;
          return (
            <Button key={cat.key} variant="outline" size="sm" onClick={() => handleCategoryChange(cat.key)} className={`rounded-lg transition-all ${category === cat.key ? "ring-2 ring-primary/50 border-primary/30 " + cat.color : "text-muted-foreground"}`}>
              {cat.icon}
              {t(cat.labelKey)}
              <Badge variant="secondary" className="text-micro-sm">{count}</Badge>
            </Button>
          );
        })}
      </div>

      <DataState loading={loading} error={error} onRetry={loadPlugins} empty={!loading && !error && filtered.length === 0} emptyIcon={Puzzle} emptyTitle={t("plugins.empty")} loadingSkeleton={<PageSkeleton />}>
      {viewMode === "grid" ? (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {paginatedFiltered.map((p, i) => <PluginCard key={p.id || String(i)} plugin={p} actionState={actionStates[p.id || String(i)]} onInstall={handleInstall} onUninstall={handleUninstall} onDelete={handleDelete} onToggle={handleToggle} onDetail={() => { setDetailPlugin(p); handleLoadDependencies(p); }} onExecute={() => { setExecutePlugin(p); setShowExecute(true); setExecuteResult(null); }} onExport={() => handleExport(p.id || "")} onUpdate={() => handleUpdate(p.id || "")} onReviews={() => handleLoadReviews(p)} onRating={(r) => handleRating(p.id || "", r)} />)}
        </div>
      ) : (
        <div className="space-y-3">
          {paginatedFiltered.map((p, i) => <PluginListItem key={p.id || String(i)} plugin={p} actionState={actionStates[p.id || String(i)]} onInstall={handleInstall} onUninstall={handleUninstall} onDelete={handleDelete} onToggle={handleToggle} onDetail={() => { setDetailPlugin(p); handleLoadDependencies(p); }} onExecute={() => { setExecutePlugin(p); setShowExecute(true); setExecuteResult(null); }} onExport={() => handleExport(p.id || "")} onUpdate={() => handleUpdate(p.id || "")} onReviews={() => handleLoadReviews(p)} onRating={(r) => handleRating(p.id || "", r)} />)}
        </div>
      )}
      </DataState>

      <Pagination page={currentPage} pageSize={PAGE_SIZE} total={filtered.length} onPageChange={setPage} />

      {detailPlugin && <PluginDetailModal plugin={detailPlugin} open={!!detailPlugin} onOpenChange={(open) => { if (!open) setDetailPlugin(null); }} />}

      <Dialog open={showCreate} onOpenChange={setShowCreate}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t("plugins.create_title")}</DialogTitle>
          </DialogHeader>
          <form onSubmit={handleCreate} className="space-y-3">
            <Input aria-label={t("plugins.name_ph")} name="plugin-name-1" type="text" required placeholder={t("plugins.name_ph")} value={createForm.name} onChange={(e) => setCreateForm({ ...createForm, name: e.target.value })} />
            <Textarea aria-label={t("plugins.desc_ph")} name="description-2" placeholder={t("plugins.desc_ph")} value={createForm.description} onChange={(e) => setCreateForm({ ...createForm, description: e.target.value })} className="min-h-[5rem] resize-none" />
            <Input aria-label={t("plugins.author_ph")} name="author-3" type="text" placeholder={t("plugins.author_ph")} value={createForm.author} onChange={(e) => setCreateForm({ ...createForm, author: e.target.value })} />
            <Select value={createForm.category} onValueChange={(v) => setCreateForm({ ...createForm, category: v ?? "" })}>
              <SelectTrigger className="w-full">
                <SelectValue placeholder={t("plugins.select_category")} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="">{t("plugins.select_category")}</SelectItem>
                {PLUGIN_CATEGORIES.slice(1).map((c) => <SelectItem key={c.key} value={c.key}>{t(c.labelKey)}</SelectItem>)}
              </SelectContent>
            </Select>
            <Input aria-label={t("plugins.version_ph")} name="version-e-g-1-0-0-5" type="text" placeholder="1.0.0" value={createForm.version} onChange={(e) => setCreateForm({ ...createForm, version: e.target.value })} />
            <Button type="submit" className="w-full h-10">{t("plugins.create_title")}</Button>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={showImport} onOpenChange={setShowImport}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t("plugins.import_title")}</DialogTitle>
          </DialogHeader>
          <Card className="border-2 border-dashed hover:border-primary transition-colors">
            <CardContent className="p-(--card-spacing) text-center">
              <CloudUpload className="size-4" />
              <p className="text-sm text-muted-foreground mb-3">{t("plugins.upload_desc")}</p>
              <input aria-label={t("plugins.upload_file")} name="input-6" type="file" accept=".json,.zip" onChange={(e) => setImportFile(e.target.files?.[0] || null)} className="text-sm text-muted-foreground file:mr-3 file:py-2 file:px-4 file:rounded-lg file:border-0 file:text-sm file:font-medium file:bg-primary/10 file:text-primary hover:file:bg-primary/15" />
              {importFile && <p className="text-xs text-muted-foreground mt-2">{importFile.name}</p>}
            </CardContent>
          </Card>
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowImport(false)}>{t("plugins.cancel")}</Button>
            <Button onClick={handleImport}>{t("plugins.import")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={showExecute} onOpenChange={(open) => { setShowExecute(open); if (open) loadExecuteAgents(); }}>
        <DialogContent size="lg" className="max-h-[80vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{t("plugins.execute_title")} {executePlugin?.Name || executePlugin?.name}</DialogTitle>
          </DialogHeader>
          <div className="space-y-3 mb-4">
            <label className="text-sm font-medium">{t("plugins.execute_agent") || "Target Beacon"}</label>
            <Select value={executeAgentId} onValueChange={(v) => setExecuteAgentId(v ?? "")}>
              <SelectTrigger className="w-full" aria-label={t("plugins.execute_agent") || "Target Beacon"}>
                <SelectValue placeholder={t("plugins.execute_agent_ph") || "Select target beacon"} />
              </SelectTrigger>
              <SelectContent>
                {executeAgents.map((a) => (
                  <SelectItem key={a.id} value={a.id}>{a.id}{a.hostname ? ` (${a.hostname})` : ""}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <Button onClick={() => handleExecute(executePlugin?.ID || executePlugin?.id || "")} disabled={executeLoading} className="w-full mb-4">
            {executeLoading ? <><Spinner className="mr-2" />{t("plugins.running")}</> : <><Play className="size-4" />{t("plugins.run_plugin")}</>}
          </Button>
          {executeResult && (
            <div className="bg-card rounded-lg p-4 max-h-96 overflow-y-auto">
              <pre className="text-xs font-mono text-success-ink whitespace-pre-wrap">{executeResult}</pre>
            </div>
          )}
        </DialogContent>
      </Dialog>

      {reviewsPlugin && (
        <ReviewsModal plugin={reviewsPlugin} reviews={reviews} open={showReviews} onOpenChange={setShowReviews} onPost={handlePostReview} />
      )}
      {modal}
    </PageContainer>
  );
}
