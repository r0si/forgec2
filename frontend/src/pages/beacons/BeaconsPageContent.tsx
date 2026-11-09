import { registerFragment } from "@/lib/i18n";
import { enAgents } from "@/lib/i18n/fragments/en.agents";
import { enCred } from "@/lib/i18n/fragments/en.cred";
import { enGenerate } from "@/lib/i18n/fragments/en.generate";
registerFragment(enAgents);
registerFragment(enCred);
registerFragment(enGenerate);

import { useState, useEffect, useCallback, useMemo, useRef, lazy, Suspense } from "react";
import { useNavigate } from "react-router-dom";
import { useWS } from "@/lib/wsContext";
import { Link } from "react-router-dom";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useVisibleInterval } from "@/lib/hooks/useVisibleInterval";
import { errorText } from "@/lib/errors";
import { POLL } from "@/lib/polling";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { DataError } from "@/components/ui/data-state";
import { PageContainer } from "@/components/ui/page-container";
import { Pagination } from "@/components/ui/pagination";
import { AgentFilters } from "./components/AgentFilters";
import { AgentBulkBar } from "./components/AgentBulkBar";
import { AgentGrid } from "./components/AgentGrid";
import { AgentContextMenu } from "./components/AgentContextMenu";
import AgentsTable from "./components/AgentsTable";
import AgentsToolbar from "./components/AgentsToolbar";
import AgentsConfirmDialogs from "./components/AgentsConfirmDialogs";
import { useAgentFilters } from "./components/useAgentFilters";
import { useAgentSelection } from "./components/useAgentSelection";
import { useAgentData } from "./components/useAgentData";
import { useAgentModals } from "./components/useAgentModals";
import { useAgentBulkOps } from "./components/useAgentBulkOps";
import { useAgentMenuAction } from "./components/useAgentMenuAction";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import ErrorBoundary from "@/components/ErrorBoundary";
import SavedViewPicker from "@/components/SavedViewPicker";

import type { Beacon, BulkResult } from "./components/types";

// Detail view (+15 sections) loads on demand so the list chunk stays lean.
const BeaconDetailPage = lazy(() => import("./detail/BeaconDetailPage"));
import type { AgentMenuPoint } from "./components/agent-menu-actions";
import { useVirtualWindow } from "@/lib/hooks/useVirtualWindow";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { useI18n } from "@/lib/i18n";
import { AlertTriangle, ArrowDown, ArrowUp, ArrowUpDown, Plus, Radio } from "lucide-react";
import { useAppStore } from "@/lib/store";

export type { Beacon };

export default function AgentsPageContent() {
  const { t } = useI18n();
  const isMobile = useAppStore((state) => state.isMobile);
  const navigate = useNavigate();
  const { subscribe, connected } = useWS();
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  const {
    beacons, setBeacons, loading, total, error, setError,
    allTags, tagsListError, refreshTagsList, tagsByAgent, tagsError,
    taskCountMap, agentLocks, setAgentLocks,
    locksLoaded, locksError, locksUnread,
    operatorPresence, setOperatorPresence,
    loadBeacons: loadBeaconsRaw, loadLocks,
  } = useAgentData(t);

  const metaFailures: string[] = [];
  if (locksError) {
    metaFailures.push(locksLoaded ? t("agents.locks_stale") : t("agents.locks_unreadable"));
  }
  if (tagsListError) metaFailures.push(t("agents.tag_list_unreadable"));
  if (tagsError) metaFailures.push(t("agents.agent_tags_unreadable"));

  const {
    confirm, setConfirm, cmdModalOpen, cmdType, cmdText, setCmdType, setCmdText,
    closeCmdModal, openCmdModal,
    quickSleepAgent, closeQuickSleep, openQuickSleep, sleepInterval, sleepJitter, setSleepInterval, setSleepJitter,
    editingNotesId, closeNotesEdit, openNotesEdit, editNotesText, setNotesText,
    screenshotConfirmOpen, setScreenshotConfirm,
    batchSleepOpen, closeBatchSleep, openBatchSleep, batchSleepInterval, batchSleepJitter, setBatchSleepInterval, setBatchSleepJitter,
  } = useAgentModals();

  const [selectedAgentId, setSelectedAgentId] = useState<string | null>(null);
  const [menuPoint, setMenuPoint] = useState<AgentMenuPoint | null>(null);
  const handleCloseDetail = useCallback(() => setSelectedAgentId(null), []);
  const handleSelectAgent = useCallback((id: string) => setSelectedAgentId(id), []);

  const [bulkMode, setBulkMode] = useState(false);
  const [actionMsg, setActionMsg] = useState<string | null>(null);
  const [showResults, setShowResults] = useState(false);
  const [bulkResults, setBulkResults] = useState<BulkResult[]>([]);

  const {
    searchInput, setSearchInput, searchQuery,
    statusFilter, setStatusFilter, osFilter, setOsFilter,
    page, setPage, sortKey, sortDir, toggleSort, setSortKey, setSortDir,
    linkedFilter, setLinkedFilter, tagFilter, setTagFilter,
    autoRefresh, setAutoRefresh, viewMode, setViewMode,
    groupByHost, setGroupByHost,
    visibleCols, setVisibleCols,
    sortedBeacons,
  } = useAgentFilters(beacons);
  const { selected, setSelected, toggleSelect, toggleSelectAll } = useAgentSelection(beacons);

  // Keep the bulk selection honest: prune ids that left the current list
  // (page change, filter applied, agent deleted) so the bulk bar never
  // claims invisible rows.
  const visibleIds = useMemo(() => new Set(sortedBeacons.map((b) => b.id || "").filter(Boolean)), [sortedBeacons]);
  useEffect(() => {
    setSelected((prev) => {
      let pruned = false;
      const next = new Set<string>();
      for (const id of prev) {
        if (visibleIds.has(id)) next.add(id);
        else pruned = true;
      }
      return pruned ? next : prev;
    });
  }, [visibleIds, setSelected]);

  const AGENT_ROW_H = 56;
  // Hardcoded 56px was ~40% taller than the real rows (py-1 + content), so
  // spacer rows drifted from actual layout while scrolling. Measure the
  // first rendered row instead.
  const [rowHeight, setRowHeight] = useState(AGENT_ROW_H);
  const rowHeightRef = useRef(AGENT_ROW_H);
  const {
    scrollRef: agentScrollRef,
    onScroll: onAgentScroll,
    virtualized: agentVirtualized,
    start: agentVirtStart,
    end: agentVirtEnd,
    offsetTop: agentOffsetTop,
    totalHeight: agentTotalHeight,
  } = useVirtualWindow({ count: sortedBeacons.length, rowHeight, threshold: 25 });

  useEffect(() => {
    if (!agentVirtualized) return;
    const el = agentScrollRef.current;
    if (!el) return;
    const probe = () => {
      const tr = el.querySelector<HTMLElement>("tbody tr[data-agent-id]");
      if (tr && tr.offsetHeight > 0 && Math.abs(tr.offsetHeight - rowHeightRef.current) > 1) {
        rowHeightRef.current = tr.offsetHeight;
        setRowHeight(tr.offsetHeight);
      }
    };
    probe();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(probe);
    ro.observe(el);
    return () => ro.disconnect();
  }, [agentVirtualized, agentScrollRef]);

  const visibleBeacons = useMemo(() => {
    // Host grouping needs the full ordered list (header rows break fixed-height
    // virtualization), so force non-virtualized rendering while grouped.
    if (groupByHost || !agentVirtualized) return sortedBeacons;
    return sortedBeacons.slice(agentVirtStart, agentVirtEnd);
  }, [sortedBeacons, groupByHost, agentVirtualized, agentVirtStart, agentVirtEnd]);

  const loadBeacons = useCallback(() => {
    loadBeaconsRaw(searchQuery, statusFilter, osFilter, page, 50, tagFilter, linkedFilter, sortKey, sortDir);
  }, [loadBeaconsRaw, searchQuery, statusFilter, osFilter, page, tagFilter, linkedFilter, sortKey, sortDir]);

  const loadBeaconsRef = useRef(loadBeacons);
  loadBeaconsRef.current = loadBeacons;
  const onlineReloadTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => { loadBeacons(); }, [loadBeacons]);
  useEffect(() => { loadLocks(); }, [loadLocks]);

  // WS events already reload/merge this list (agent_online/offline/data_update);
  // the 30s poll is a fallback for when the socket is down.
  useVisibleInterval(() => loadBeacons(), autoRefresh ? (connected ? POLL.wsDrift : POLL.agents) : 0);

  useEffect(() => {
    if (!actionMsg) return;
    const timer = setTimeout(() => setActionMsg(null), 5000);
    return () => clearTimeout(timer);
  }, [actionMsg]);

  useEffect(() => {
    if (!showResults) return;
      api.get(paths.agents.bulkResults())
      .then((d) => {
        if (!mountedRef.current) return;
        const results = (d.results || d.data || []) as BulkResult[];
        setBulkResults(results);
      })
      .catch((e) => { if (mountedRef.current) setActionMsg(errorText(t, e, () => t("agents.bulk_results_failed"))); });
  }, [showResults, t]);

  useEffect(() => {
    const unsub = subscribe((msg) => {
      if (msg.type === "agent_online" || msg.type === "agent_offline") {
        if (onlineReloadTimer.current) clearTimeout(onlineReloadTimer.current);
        onlineReloadTimer.current = setTimeout(() => loadBeaconsRef.current(), 500);
      } else if (msg.type === "agent_data_update" && msg.agent_id) {
        const aid = String(msg.agent_id);
        setBeacons((prev) => prev.map((b) =>
          (b.id || "") === aid ? { ...b, ...(msg.data as Partial<Beacon>) } : b
        ));
      } else if (msg.type === "agent_locked" && msg.agent_id) {
        setAgentLocks((prev) => ({ ...prev, [String(msg.agent_id)]: String(msg.username) }));
      } else if (msg.type === "agent_unlocked" && msg.agent_id) {
        setAgentLocks((prev) => {
          const next = { ...prev };
          delete next[String(msg.agent_id)];
          return next;
        });
      } else if (msg.type === "operator_presence") {
        const ops = (msg.operators as { user?: string; agent_id?: string }[]) || [];
        const presence: Record<string, string[]> = {};
        for (const op of ops) {
          if (op.user && op.agent_id) {
            if (!presence[op.agent_id]) presence[op.agent_id] = [];
            presence[op.agent_id].push(op.user);
          }
        }
        setOperatorPresence(presence);
      }
    });
    return () => {
      if (onlineReloadTimer.current) clearTimeout(onlineReloadTimer.current);
      unsub();
    };
    // loadBeaconsRef always points at latest loadBeacons — intentional stable subscribe
  }, [subscribe, setAgentLocks, setBeacons, setOperatorPresence]);

  const sortIcon = (field: typeof sortKey) => {
    if (sortKey !== field) return <ArrowUpDown className="size-3 text-muted-foreground" />;
    return sortDir === "asc" ? <ArrowUp className="size-3 text-primary" /> : <ArrowDown className="size-3 text-primary" />;
  };

  const handleSortKeyDown = useCallback(
    (field: typeof sortKey) => (e: React.KeyboardEvent) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        toggleSort(field);
      }
    },
    [toggleSort],
  );

  const bulkOps = useAgentBulkOps({
    t, selected, setSelected, loadBeacons, setActionMsg, setConfirm,
    setScreenshotConfirm, openBatchSleep, closeBatchSleep, batchSleepInterval, batchSleepJitter,
    cmdType, cmdText, closeCmdModal,
    quickSleepAgent, sleepInterval, sleepJitter, closeQuickSleep,
    editingNotesId, editNotesText, closeNotesEdit,
    searchQuery, statusFilter, osFilter, tagFilter, linkedFilter, sortKey, sortDir,
  });
  const {
    exporting, batchScreenshot, batchSleep,
    killAgent, deleteAgent, uninstallAgent, batchDelete, bulkKill, bulkUninstall,
    handleCmdSubmit, submitQuickSleep, submitNotes, doBatchScreenshot, doBatchSleep, exportCSV,
  } = bulkOps;

  const { handleMenuAction, handleQuickNav } = useAgentMenuAction({
    t, navigate, setSelectedAgentId, setMenuPoint, openQuickSleep, openNotesEdit, setConfirm,
  });

  const counts = useMemo(() => {
    let online = 0, stale = 0, offline = 0, windows = 0, linux = 0, darwin = 0;
    for (const b of beacons) {
      if (b.status === "online") online++;
      else if (b.status === "stale") stale++;
      else if (b.status === "offline") offline++;
      const os = (b.os || "").toLowerCase();
      if (os.includes("windows")) windows++;
      else if (os.includes("linux")) linux++;
      else if (os.includes("darwin")) darwin++;
    }
    return { onlineCount: online, staleCount: stale, offlineCount: offline, windowsCount: windows, linuxCount: linux, darwinCount: darwin };
  }, [beacons]);

  const { onlineCount, staleCount, offlineCount, windowsCount, linuxCount, darwinCount } = counts;
  const effectiveViewMode = isMobile ? "grid" : viewMode;

  const emptyColSpan = Object.values(visibleCols).filter(Boolean).length + 2;

  return (
    <PageContainer
      title={t("agents.title")}
      subtitle={`${total} ${t("agents.total_label")} · ${onlineCount} ${t("nav.online_label")}${staleCount > 0 ? `, ${staleCount} ${t("agents.stale_label")}` : ""}${offlineCount > 0 ? `, ${offlineCount} ${t("agents.offline_label")}` : ""}`}
      actions={<AgentsToolbar
        t={t}
        bulkMode={bulkMode}
        onToggleBulk={() => { setBulkMode((p) => !p); if (!bulkMode) setSelected(new Set()); }}
        showResults={showResults}
        onToggleResults={() => { setShowResults((p) => !p); if (!showResults) setBulkMode(true); }}
        exporting={exporting}
        onExport={() => void exportCSV()}
        autoRefresh={autoRefresh}
        onToggleAutoRefresh={() => setAutoRefresh((p) => !p)}
        effectiveViewMode={effectiveViewMode}
        onToggleView={() => setViewMode((p) => p === "table" ? "grid" : "table")}
        groupByHost={groupByHost}
        onToggleGroupByHost={() => setGroupByHost((p) => !p)}
        onRefresh={() => { setPage(1); loadBeacons(); }}
      />}
    >
      {error && (
        <DataError
          message={error}
          onRetry={() => { setError(null); loadBeacons(); }}
          onDismiss={() => setError(null)}
          className="mb-4"
        />
      )}

      {metaFailures.length > 0 && (
        <div role="alert" className="mb-4 flex flex-wrap items-center gap-3 rounded-xl border border-warning/30 bg-warning/10 px-4 py-3 text-xs text-warning-ink">
          <AlertTriangle className="size-4 shrink-0" aria-hidden="true" />
          <span className="min-w-0 flex-1">{t("agents.meta_partial_failed", { sources: metaFailures.join(", ") })}</span>
          {locksError && (
            <Button onClick={loadLocks} size="sm" variant="outline" className="min-h-11 px-4 sm:min-h-7 sm:px-2.5">{t("common.try_again")}</Button>
          )}
          {(tagsListError || tagsError) && (
            <Button onClick={() => { void refreshTagsList(); }} size="sm" variant="outline" className="min-h-11 px-4 sm:min-h-7 sm:px-2.5">{t("common.try_again")}</Button>
          )}
        </div>
      )}

      <AgentBulkBar
        selected={selected}
        bulkMode={bulkMode}
        bulkResults={bulkResults}
        showResults={showResults}
        setShowResults={setShowResults}
        onBulkShell={openCmdModal}
        onBulkScreenshot={batchScreenshot}
        onBulkBeaconNow={() => {
          const ids = Array.from(selected);
          if (!ids.length) return;
          void bulkOps.bulkTask(ids, "beacon_now", "");
        }}
        onBulkSleep={batchSleep}
        onBulkKill={() => setConfirm({ type: "bulk-kill" })}
        onBulkUninstall={() => setConfirm({ type: "bulk-uninstall" })}
        onBulkDelete={() => setConfirm({ type: "batch-delete" })}
        onClearSelection={() => setSelected(new Set())}
        actionMsg={actionMsg}
        dismissActionMsg={() => setActionMsg(null)}
      />

      <AgentFilters
        searchInput={searchInput}
        setSearchInput={setSearchInput}
        statusFilter={statusFilter}
        setStatusFilter={(v) => { setStatusFilter(v); setPage(1); }}
        osFilter={osFilter}
        setOsFilter={(v) => { setOsFilter(v); setPage(1); }}
        tagFilter={tagFilter}
        setTagFilter={(v) => { setTagFilter(v); setPage(1); }}
        linkedFilter={linkedFilter}
        setLinkedFilter={(v) => { setLinkedFilter(v as "" | "direct" | "chained"); setPage(1); }}
        allTags={allTags}
        visibleCols={visibleCols}
        setVisibleCols={setVisibleCols}
        onlineCount={onlineCount}
        windowsCount={windowsCount}
        linuxCount={linuxCount}
        darwinCount={darwinCount}
      />

      <SavedViewPicker
        page="agents"
        getState={() => ({
          search: searchInput,
          status: statusFilter,
          os: osFilter,
          tag: tagFilter,
          linked: linkedFilter,
          sortKey,
          sortDir,
          viewMode,
          groupByHost,
          page_size_page: page,
        })}
        applyState={(s) => {
          if (typeof s.search === "string") setSearchInput(s.search);
          if (typeof s.status === "string") setStatusFilter(s.status);
          if (typeof s.os === "string") setOsFilter(s.os);
          if (typeof s.tag === "string") setTagFilter(s.tag);
          if (typeof s.linked === "string") setLinkedFilter(s.linked as "" | "direct" | "chained");
          if (s.sortKey === "hostname" || s.sortKey === "username" || s.sortKey === "os" || s.sortKey === "ip" || s.sortKey === "last_seen" || s.sortKey === "status") {
            setSortKey(s.sortKey);
          }
          if (s.sortDir === "asc" || s.sortDir === "desc") setSortDir(s.sortDir);
          if (s.viewMode === "table" || s.viewMode === "grid") setViewMode(s.viewMode);
          setGroupByHost(s.groupByHost === true);
          setPage(1);
        }}
      />

      {effectiveViewMode === "table" && (
      <AgentsTable
        t={t}
        beacons={beacons}
        visibleBeacons={visibleBeacons}
        selected={selected}
        toggleSelect={toggleSelect}
        toggleSelectAll={toggleSelectAll}
        sortKey={sortKey}
        sortDir={sortDir}
        toggleSort={toggleSort}
        sortIcon={sortIcon}
        handleSortKeyDown={handleSortKeyDown}
        visibleCols={visibleCols}
        loading={loading}
        emptyColSpan={emptyColSpan}
        agentVirtualized={agentVirtualized && !groupByHost}
        agentScrollRef={agentScrollRef}
        onAgentScroll={onAgentScroll}
        agentOffsetTop={agentOffsetTop}
        agentTotalHeight={agentTotalHeight}
        rowHeight={rowHeight}
        statusFilter={statusFilter}
        osFilter={osFilter}
        page={page}
        total={total}
        setPage={setPage}
        onSelectAgent={handleSelectAgent}
        onMenu={setMenuPoint}
        onQuickNav={handleQuickNav}
        onEditNotes={openNotesEdit}
        taskCountMap={taskCountMap}
        agentLocks={agentLocks}
        locksUnread={locksUnread}
        operatorPresence={operatorPresence}
        tagsByAgent={tagsByAgent}
        groupByHost={groupByHost}
      />
      )}

      {effectiveViewMode === "grid" && loading && (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-3 p-4">
          {Array.from({ length: 8 }).map((_, i) => (
            <Card key={`gskel-${i}`} className="p-(--card-spacing) space-y-3">
              <div className="flex items-center gap-2.5">
                <Skeleton className="size-10 rounded-xl" />
                <div className="space-y-1.5 flex-1"><Skeleton className="h-4 w-24" /><Skeleton className="h-3 w-16" /></div>
              </div>
              <Skeleton className="h-3 w-full" />
              <Skeleton className="h-3 w-2/3" />
              <Skeleton className="h-3 w-1/2" />
            </Card>
          ))}
        </div>
      )}

      {effectiveViewMode === "grid" && !loading && beacons.length === 0 && (
        <Card className="overflow-hidden">
          <EmptyState
            icon={Radio}
            title={t("agents.no_beacons")}
            message={statusFilter || osFilter ? t("agents.no_beacons_filtered") : t("agents.no_beacons_hint")}
            action={!statusFilter && !osFilter ? (
              <Button render={<Link to="/payload-generator" />}>
                <Plus className="size-4" />
                <span>{t("agents.generate_implant")}</span>
              </Button>
            ) : undefined}
          />
        </Card>
      )}

      {effectiveViewMode === "grid" && !loading && beacons.length > 0 && (
        <>
          <AgentGrid
            beacons={sortedBeacons}
            tagsByAgent={tagsByAgent}
            taskCountMap={taskCountMap}
            onInteract={handleSelectAgent}
            onDetails={handleSelectAgent}
            onMenu={setMenuPoint}
            selected={selected}
            onToggleSelect={toggleSelect}
          />
          <Pagination page={page} pageSize={50} total={total} onPageChange={setPage} />
        </>
      )}

      <AgentsConfirmDialogs
        t={t}
        confirm={confirm}
        setConfirm={setConfirm}
        selectedSize={selected.size}
        killAgent={(id) => void killAgent(id)}
        deleteAgent={(id) => void deleteAgent(id)}
        uninstallAgent={(id) => void uninstallAgent(id)}
        batchDelete={() => void batchDelete()}
        bulkKill={() => void bulkKill()}
        bulkUninstall={() => void bulkUninstall()}
        cmdModalOpen={cmdModalOpen}
        cmdType={cmdType}
        setCmdType={setCmdType}
        cmdText={cmdText}
        setCmdText={setCmdText}
        handleCmdSubmit={() => void handleCmdSubmit()}
        closeCmdModal={closeCmdModal}
        quickSleepAgent={quickSleepAgent}
        sleepInterval={sleepInterval}
        setSleepInterval={setSleepInterval}
        sleepJitter={sleepJitter}
        setSleepJitter={setSleepJitter}
        submitQuickSleep={() => void submitQuickSleep()}
        closeQuickSleep={closeQuickSleep}
        editingNotesId={editingNotesId}
        editNotesText={editNotesText}
        setNotesText={setNotesText}
        submitNotes={() => void submitNotes()}
        closeNotesEdit={closeNotesEdit}
        screenshotConfirmOpen={screenshotConfirmOpen}
        setScreenshotConfirm={setScreenshotConfirm}
        doBatchScreenshot={doBatchScreenshot}
        batchSleepOpen={batchSleepOpen}
        batchSleepInterval={batchSleepInterval}
        setBatchSleepInterval={setBatchSleepInterval}
        batchSleepJitter={batchSleepJitter}
        setBatchSleepJitter={setBatchSleepJitter}
        doBatchSleep={doBatchSleep}
        closeBatchSleep={closeBatchSleep}
      />

      <Sheet open={!!selectedAgentId} onOpenChange={(open) => { if (!open) setSelectedAgentId(null); }}>
        <SheetContent
          side="right"
          showCloseButton={false}
          className="h-full w-full gap-0 overflow-hidden bg-background p-0 text-base sm:max-w-[min(96rem,92vw)]"
        >
          <ErrorBoundary>
            {selectedAgentId && <Suspense fallback={null}><BeaconDetailPage key={selectedAgentId} agentId={selectedAgentId} onClose={handleCloseDetail} /></Suspense>}
          </ErrorBoundary>
        </SheetContent>
      </Sheet>

      <AgentContextMenu
        point={menuPoint}
        onClose={() => setMenuPoint(null)}
        onAction={(action) => { if (menuPoint) handleMenuAction(action, menuPoint); }}
      />
    </PageContainer>
  );
}
