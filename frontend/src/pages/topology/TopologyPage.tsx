
import { registerFragment } from "@/lib/i18n";
import { enTopology } from "@/lib/i18n/fragments/en.topology";
registerFragment(enTopology);

import { useEffect, useState, useCallback, useRef, lazy, Suspense } from "react";

import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";
import { useVisibleInterval } from "@/lib/hooks/useVisibleInterval";
import { POLL } from "@/lib/polling";
import { exportElementPng } from "@/lib/chartExport";
import { nowTime } from "@/lib/utils";
import { PageContainer } from "@/components/ui/page-container";
import { Card } from "@/components/ui/card";
import { StatusDot } from "@/components/ui/status-dot";
import ErrorBoundary from "@/components/ErrorBoundary";
import { DataError } from "@/components/ui/data-state";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { Download, Globe2, Info, List, Menu, MousePointerClick, RotateCw, Snowflake, Zap } from "lucide-react";
import type { TopoNode, TopoEdge, TopoData, NetTopologyData } from "@/types/topology";

const TopologyGraph = lazy(() => import("@/components/TopologyGraph"));

type TopoViewMode = "c2" | "mesh" | "net";

interface TopologySidebarProps {
  selectedTopoNode: TopoNode | null;
  selectedNode: TopoNode | null;
  setParentId: string;
  onSetParentId: (v: string) => void;
  handleSetRoute: () => void;
  routeMsg: string;
  onlineCount: number;
  offlineCount: number;
  listenerCount: number;
  meshNodeCount: number;
  useMeshSource: boolean;
  useNetSource?: boolean;
  netStats?: NetTopologyData["stats"];
  physicsEnabled: boolean;
  togglePhysics: () => void;
  exportPng: () => void;
}

function TopologySidebar({
  selectedTopoNode,
  selectedNode,
  setParentId,
  onSetParentId,
  handleSetRoute,
  routeMsg,
  onlineCount,
  offlineCount,
  listenerCount,
  meshNodeCount,
  useMeshSource,
  useNetSource = false,
  netStats,
  physicsEnabled,
  togglePhysics,
  exportPng,
}: TopologySidebarProps) {
  const { t } = useI18n();
  return (
    <>
      <Card className="overflow-hidden p-0 shadow-sm hover:shadow-md transition-shadow duration-200">
        <div className="bg-primary/10 border border-primary/20 px-4 py-2.5">
          <div className="flex items-center gap-2">
            <Info className="size-4" />
            <span className="text-xs font-semibold text-foreground">{t("topology.node_details")}</span>
          </div>
        </div>
        <div className="p-4">
          {selectedTopoNode ? (
            <div className="space-y-2">
              <div className="font-semibold text-sm text-foreground">
                {selectedTopoNode.label || selectedTopoNode.id}
              </div>
              <div className="text-xs text-muted-foreground">
                <div><b>ID:</b> {selectedTopoNode.id}</div>
                <div><b>{t("topology.group")}:</b> {selectedTopoNode.group}</div>
                {selectedTopoNode.p2p_mode && (
                  <div><b>{t("topology.p2p_mode")}</b> {selectedTopoNode.p2p_mode}</div>
                )}
                <div><b>{t("topology.peers")}:</b> {selectedTopoNode.peer_count ?? "?"}</div>
                {selectedTopoNode.title && (
                  <div className="mt-1 whitespace-pre-line">{selectedTopoNode.title}</div>
                )}
              </div>
              <div className="mt-3">
                <span className="text-xs text-muted-foreground block mb-1">{t("topology.set_parent")}</span>
                <div className="flex gap-1">
                  <Input
                    aria-label={t("topology.parent_agent_id")} name="input-1"
                    value={setParentId}
                    onChange={(e) => onSetParentId(e.target.value)}
                    placeholder={t("topology.parent_agent_id")}
                    className="flex-1 text-xs"
                  />
                  <Button
                    size="xs"
                    onClick={handleSetRoute}
                  >
                    {t("topology.set")}
                  </Button>
                </div>
                {routeMsg && (
                  <p className={`text-xs mt-1 ${routeMsg.includes("success") ? "text-success-ink" : "text-destructive-ink"}`}>
                    {routeMsg}
                  </p>
                )}
              </div>
            </div>
          ) : selectedNode ? (
            <div className="space-y-2">
              <div className="font-semibold text-sm text-foreground">{selectedNode.label}</div>
              <div className="text-xs text-muted-foreground">{selectedNode.title?.replace(/<br\s*\/?>/g, "\n")}</div>
            </div>
          ) : (
            <div className="text-center py-6 text-muted-foreground">
              <MousePointerClick className="size-4 mx-auto" />
              <p className="text-xs">{t("topology.click_hint")}</p>
            </div>
          )}
        </div>
      </Card>

      <Card className="overflow-hidden p-0 shadow-sm hover:shadow-md transition-shadow duration-200">
        <div className="bg-secondary/60 border border-border px-4 py-2.5">
          <div className="flex items-center gap-2">
            <List className="size-4" />
            <span className="text-xs font-semibold text-foreground">{t("topology.legend")}</span>
          </div>
        </div>
        <div className="p-4 space-y-3">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <StatusDot tone="success" size="md" className="shadow-lg shadow-success/50" />
              <span className="text-xs text-muted-foreground">{t("topology.legend_online")}</span>
            </div>
            <span className="text-xs font-mono text-muted-foreground">{onlineCount}</span>
          </div>
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <StatusDot tone="muted" size="md" />
              <span className="text-xs text-muted-foreground">{t("topology.legend_offline")}</span>
            </div>
            <span className="text-xs font-mono text-muted-foreground">{offlineCount}</span>
          </div>
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <span className="size-4 rounded bg-primary/100 shadow-lg shadow-primary/50 rotate-45"></span>
              <span className="text-xs text-muted-foreground">{t("topology.legend_listener")}</span>
            </div>
            <span className="text-xs font-mono text-muted-foreground">{listenerCount}</span>
          </div>
          {useMeshSource && (
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <span className="size-3 rounded-full bg-chart-6"></span>
                <span className="text-xs text-muted-foreground">{t("topology.legend_peer")}</span>
              </div>
              <span className="text-xs font-mono text-muted-foreground">{meshNodeCount}</span>
            </div>
          )}
          {useNetSource && (
            <>
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <span className="size-3 rounded-full bg-chart-5"></span>
                  <span className="text-xs text-muted-foreground">{t("topology.legend_host_lateral")}</span>
                </div>
                <span className="text-xs font-mono text-muted-foreground">{netStats?.hosts_lateral ?? 0}</span>
              </div>
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <span className="size-3 rounded-full bg-chart-2"></span>
                  <span className="text-xs text-muted-foreground">{t("topology.legend_host_discovered")}</span>
                </div>
                <span className="text-xs font-mono text-muted-foreground">{netStats?.hosts_discovered ?? 0}</span>
              </div>
              {!!netStats?.merged_into_agent && (
                <p className="text-micro-sm text-muted-foreground">
                  {t("topology.legend_merged", { n: String(netStats.merged_into_agent) })}
                </p>
              )}
              <div className="border-t border-border pt-3">
                <div className="flex items-center gap-2">
                  <span className="w-6 h-0.5 border-t-2 border-dashed border-warning"></span>
                  <span className="text-xs text-muted-foreground">{t("topology.edge_p2p")}</span>
                </div>
                <div className="flex items-center gap-2 mt-2">
                  <span className="w-6 h-0.5 border-t-2 border-dotted border-orange-400"></span>
                  <span className="text-xs text-muted-foreground">{t("topology.edge_proxy")}</span>
                </div>
                <div className="flex items-center gap-2 mt-2">
                  <span className="w-6 h-0.5 border-t-2 border-dashed border-slate-500"></span>
                  <span className="text-xs text-muted-foreground">{t("topology.edge_discovered")}</span>
                </div>
              </div>
            </>
          )}
          <div className="border-t border-border pt-3">
            <div className="flex items-center gap-2 mb-2">
              <span className="w-6 h-0.5 bg-muted-foreground"></span>
              <span className="text-xs text-muted-foreground">{t("topology.edge_c2")}</span>
            </div>
            <div className="flex items-center gap-2">
              <span className="w-6 h-0.5 border-t-2 border-dashed border-warning/50"></span>
              <span className="text-xs text-muted-foreground">{t("topology.edge_p2p")}</span>
            </div>
            <div className="flex items-center gap-2 mt-2">
              <span className="w-6 h-0.5 border-t-2 border-dashed border-success"></span>
              <span className="text-xs text-muted-foreground">{t("topology.edge_mesh")}</span>
            </div>
          </div>
        </div>
      </Card>

      <Card className="overflow-hidden p-0 shadow-sm hover:shadow-md transition-shadow duration-200">
        <div className="bg-success/10 border border-success/20 px-4 py-2.5">
          <div className="flex items-center gap-2">
            <Zap className="size-4" />
            <span className="text-xs font-semibold text-foreground">{t("topology.quick_actions")}</span>
          </div>
        </div>
        <div className="p-4 space-y-2">
          <Button variant="ghost" size="sm" onClick={togglePhysics} className="w-full justify-between">
            <span><Snowflake className="size-4" />{t("topology.physics")}</span>
            <span className={`font-mono ${physicsEnabled ? "text-success-ink" : "text-muted-foreground"}`}>{physicsEnabled ? t("topology.on") : t("topology.off")}</span>
          </Button>
          <Button variant="ghost" size="sm" onClick={exportPng} className="w-full justify-between">
            <span><Download className="size-4" />{t("topology.export_png")}</span>
          </Button>
        </div>
      </Card>
    </>
  );
}

export default function TopologyPage() {
  const { t } = useI18n();
  const [data, setData] = useState<TopoData | null>(null);
  const [meshData, setMeshData] = useState<TopoData | null>(null);
  const [netData, setNetData] = useState<NetTopologyData | null>(null);
  const [loading, setLoading] = useState(true);
  const [selectedNode, setSelectedNode] = useState<TopoNode | null>(null);
  const [selectedTopoNode, setSelectedTopoNode] = useState<TopoNode | null>(null);
  const physicsRef = useRef(true);
  const [physicsEnabled, setPhysicsEnabled] = useState(true);
  const [viewMode, setViewMode] = useState<TopoViewMode>("mesh");
  const useMeshSource = viewMode === "mesh";
  const useNetSource = viewMode === "net";
  const [setParentId, setSetParentId] = useState("");
  const [routeMsg, setRouteMsg] = useState("");
  const [now, setNow] = useState("");
  const [mobileSidebar, setMobileSidebar] = useState(false);

  // Per-view loaded flags: switching to a never-loaded mode must show a
  // spinner (not the previous mode's graph or an empty-state flash), while
  // repeat polls of an already-loaded mode keep the graph mounted.
  const loadedRef = useRef<Partial<Record<TopoViewMode, boolean>>>({});
  const loadControllerRef = useRef<AbortController | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);

  const loadTopology = useCallback(async () => {
    loadControllerRef.current?.abort();
    const controller = new AbortController();
    loadControllerRef.current = controller;
    const { signal } = controller;
    const stale = () => signal.aborted || loadControllerRef.current !== controller;
    const firstLoad = !loadedRef.current[viewMode];
    if (firstLoad) { setLoading(true); setLoadError(null); }
    try {
      if (viewMode === "net") {
        const result = await api.get<NetTopologyData & { success?: boolean }>(paths.topology.network, { signal });
        if (stale()) return;
        setNetData({
          nodes: (result.nodes || []) as TopoNode[],
          edges: (result.edges || []) as TopoEdge[],
          stats: result.stats,
        });
      } else if (viewMode === "mesh") {
        const result = await api.get<{ success?: boolean; nodes?: TopoNode[]; edges?: TopoEdge[] }>("/mesh/topology", { signal });
        if (stale()) return;
        if (result.success) {
          setMeshData({ nodes: (result.nodes || []) as TopoNode[], edges: (result.edges || []) as TopoEdge[] });
        } else {
          setMeshData({ nodes: [] as TopoNode[], edges: [] as TopoEdge[] });
        }
      } else {
        const result = await api.get<{ Nodes?: TopoNode[]; nodes?: TopoNode[]; Edges?: Array<{ from: string; to: string }>; edges?: Array<{ from: string; to: string }>; data?: { Nodes?: TopoNode[]; nodes?: TopoNode[]; Edges?: Array<{ from: string; to: string }>; edges?: Array<{ from: string; to: string }> } }>(paths.topology.data, { signal });
        if (stale()) return;
        if (result.data) {
          setData({ nodes: (result.data.nodes || []) as TopoNode[], edges: (result.data.edges || []) as Array<{ from: string; to: string }> });
        } else {
          setData({ nodes: (result.nodes || []) as TopoNode[], edges: (result.edges || []) as Array<{ from: string; to: string }> });
        }
      }
    } catch (e) {
      if (stale()) return;
      // Keep the last good graph on transient poll failures — wiping the
      // data here would destroy/recreate the vis instance (zoom/pan reset).
      // A failed FIRST load is different: data stays null, so it must show a
      // retryable error rather than an authoritative empty graph.
      if (firstLoad) {
        setLoadError(e instanceof Error ? e.message : t("topology.load_failed"));
      }
      setLoading(false);
      return;
    }
    if (stale()) return;
    loadedRef.current[viewMode] = true;
    setLoading(false);
  }, [viewMode, t]);

  useEffect(() => {
    void loadTopology();
    return () => loadControllerRef.current?.abort();
  }, [loadTopology]);
  useVisibleInterval(() => { void loadTopology(); }, POLL.topology);

  useEffect(() => { setNow(nowTime()); }, []);

  const graphContainerRef = useRef<HTMLDivElement>(null);

  const togglePhysics = () => {
    physicsRef.current = !physicsRef.current;
    setPhysicsEnabled(physicsRef.current);
  };

  const exportPng = async () => {
    if (!graphContainerRef.current) return;
    try {
      const el = graphContainerRef.current.querySelector("div") || graphContainerRef.current;
      await exportElementPng(el, `topology-${Date.now()}.png`);
    } catch { /* ignore */ }
  };

  const handleNodeClick = useCallback((node: TopoNode | null) => {
    if (!node) {
      setSelectedNode(null);
      setSelectedTopoNode(null);
      return;
    }
    if (viewMode === "mesh") {
      setSelectedTopoNode(node);
      setSetParentId("");
      setRouteMsg("");
    } else {
      setSelectedNode(node);
    }
  }, [viewMode]);

  const handleSetRoute = async () => {
    if (!selectedTopoNode?.id || !setParentId) return;
    setRouteMsg("");
    try {
      const agentId = selectedTopoNode.id.replace("agent-", "");
      const result = await api.postJson(paths.mesh.route(agentId), { parent_id: setParentId });
      if (result.success) {
        setRouteMsg(t("topology.route_success"));
        loadTopology();
      } else {
        setRouteMsg(((result.error as string) || t("topology.route_failed")) as string);
      }
    } catch {
      setRouteMsg(t("topology.route_request_failed"));
    }
  };

  const nodes = useNetSource
    ? (netData?.nodes || [])
    : useMeshSource
      ? (meshData?.nodes || [])
      : (data?.nodes || []);
  const onlineCount = nodes.filter((n) => n.group === "agent-online").length;
  const offlineCount = nodes.filter((n) => n.group === "agent-offline").length;
  const listenerCount = nodes.filter((n) => n.group === "listener").length;
  const meshNodeCount = useMeshSource ? (meshData?.nodes || []).length : 0;
  const meshEdgeCount = useMeshSource ? (meshData?.edges || []).length : 0;
  const netStats = netData?.stats;
  const subtitle = viewMode === "net"
    ? t("topology.net_view")
    : useMeshSource ? t("topology.p2p_view") : t("topology.c2_view");

  return (
    <PageContainer title={t("topology.title")} subtitle={subtitle} actions={<>
        <div className="flex items-center gap-1.5 px-3 py-1.5 bg-muted rounded-lg">
          <StatusDot tone="success" size="sm" pulse />
          <span className="text-xs font-medium text-muted-foreground">{onlineCount} {t("topology.online")}</span>
        </div>
        <div className="flex items-center gap-1.5 px-3 py-1.5 bg-muted rounded-lg">
          <StatusDot tone="muted" size="sm" />
          <span className="text-xs font-medium text-muted-foreground">{offlineCount} {t("topology.offline")}</span>
        </div>
        {useNetSource ? (
          <>
            <div className="flex items-center gap-1.5 px-3 py-1.5 bg-muted rounded-lg">
              <Globe2 className="size-4 text-info-ink" />
              <span className="text-xs font-medium text-muted-foreground">
                {netStats?.hosts ?? 0} {t("topology.hosts")}
              </span>
            </div>
            {!!netStats?.hosts_lateral && (
              <div className="flex items-center gap-1.5 px-3 py-1.5 bg-destructive/10 rounded-lg">
                <span className="size-2 rounded-full bg-destructive" />
                <span className="text-xs font-medium text-destructive-ink">
                  {netStats.hosts_lateral} {t("topology.hosts_lateral")}
                </span>
              </div>
            )}
          </>
        ) : (
          <div className="flex items-center gap-1.5 px-3 py-1.5 bg-muted rounded-lg">
            <StatusDot tone="primary" size="sm" />
            <span className="text-xs font-medium text-muted-foreground">{listenerCount} {t("topology.listeners")}</span>
          </div>
        )}
        {useMeshSource && (
          <div className="flex items-center gap-1.5 px-3 py-1.5 bg-muted rounded-lg">
            <span className="text-xs font-medium text-muted-foreground">
              {meshNodeCount}N / {meshEdgeCount}E
            </span>
          </div>
        )}
        <div className="xl:hidden">
          <Button variant="ghost" size="icon" aria-label={t("topology.open_sidebar")} className="min-w-[44px] min-h-[44px]" onClick={() => setMobileSidebar(true)}>
            <Menu className="size-5" />
          </Button>
        </div>
        <Button variant="ghost" size="xs" onClick={() => loadTopology()} className="flex items-center gap-1.5">
          <RotateCw className="size-4" /> {t("topology.refresh")}
        </Button>
      </>}>

      <div className="grid grid-cols-1 xl:grid-cols-4 gap-4">
        <div className="xl:col-span-3">
          <Card className="overflow-hidden p-0 shadow-sm hover:shadow-md transition-shadow duration-200">
            <div className="bg-card border-b border-border px-4 py-2.5 flex items-center justify-between">
              <div className="flex items-center gap-2">
                <div className="flex gap-1">
                  <span className="size-2.5 rounded-full bg-destructive"></span>
                  <span className="size-2.5 rounded-full bg-chart-4"></span>
                  <span className="size-2.5 rounded-full bg-success"></span>
                </div>
                <span className="text-xs text-muted-foreground font-mono ml-2">network-topology://view</span>
              </div>
              <div className="flex items-center gap-2">
                <div className="flex rounded-lg bg-muted p-0.5 gap-0.5" role="tablist" aria-label={t("topology.view_mode")}>
                  {([
                    ["c2", t("topology.mode_c2")],
                    ["mesh", t("topology.mode_mesh")],
                    ["net", t("topology.mode_net")],
                  ] as Array<[TopoViewMode, string]>).map(([mode, label]) => (
                    <button
                      key={mode}
                      type="button"
                      role="tab"
                      aria-selected={viewMode === mode}
                      onClick={() => setViewMode(mode)}
                      className={`px-2.5 py-1 text-xs rounded-md transition-colors ${viewMode === mode ? "bg-background shadow-sm font-medium text-foreground" : "text-muted-foreground hover:text-foreground"}`}
                    >
                      {label}
                    </button>
                  ))}
                </div>
                <span className="text-micro-sm text-muted-foreground font-mono">{loadError ? `${t("status.unknown")} (${t("common.error")})` : `NODES: ${nodes.length}`}</span>
              </div>
            </div>
            <div ref={graphContainerRef} className="relative p-4 bg-card [background-image:radial-gradient(ellipse_at_center,var(--card)_0%,var(--background)_100%)] min-h-[500px]">
              {/* A crashing graph (bad topology payload, canvas/Graph lib
                throwing) must not blank the whole page: the sidebar and
                controls stay usable, retry remounts on view change. */}
              <ErrorBoundary resetKey={viewMode}>
                <Suspense fallback={null}>
                  {loadError ? (
                    <DataError message={t("topology.unreadable", { message: loadError })} onRetry={() => { void loadTopology(); }} />
                  ) : (
                  <TopologyGraph
                    data={data || { nodes: [], edges: [] }}
                    meshData={meshData || { nodes: [], edges: [] }}
                    netData={netData || { nodes: [], edges: [] }}
                    useMeshSource={useMeshSource}
                    useNetSource={useNetSource}
                    physicsEnabled={physicsEnabled}
                    loading={loading}
                    onNodeClick={handleNodeClick}
                  />
                  )}
                </Suspense>
              </ErrorBoundary>
            </div>
            <div className="bg-background border-t border-border px-4 py-1.5 flex items-center justify-between">
              <span className="text-micro-sm text-muted-foreground font-mono">
                {loading ? t("common.loading") : subtitle}
              </span>
              <span className="text-micro-sm text-muted-foreground font-mono">{now || "--:--:--"}</span>
            </div>
          </Card>
        </div>

        <div className="hidden xl:block space-y-4">
          <TopologySidebar
            selectedTopoNode={selectedTopoNode}
            selectedNode={selectedNode}
            setParentId={setParentId}
            onSetParentId={setSetParentId}
            handleSetRoute={handleSetRoute}
            routeMsg={routeMsg}
            onlineCount={onlineCount}
            offlineCount={offlineCount}
            listenerCount={listenerCount}
            meshNodeCount={meshNodeCount}
            useMeshSource={useMeshSource}
            useNetSource={useNetSource}
            netStats={netStats}
            physicsEnabled={physicsEnabled}
            togglePhysics={togglePhysics}
            exportPng={exportPng}
          />
        </div>
      </div>

      <Sheet open={mobileSidebar} onOpenChange={setMobileSidebar}>
        <SheetContent side="right" className="w-[320px] sm:w-[360px] overflow-y-auto">
          <div className="space-y-4 pt-8">
            <TopologySidebar
              selectedTopoNode={selectedTopoNode}
              selectedNode={selectedNode}
              setParentId={setParentId}
              onSetParentId={setSetParentId}
              handleSetRoute={handleSetRoute}
              routeMsg={routeMsg}
              onlineCount={onlineCount}
              offlineCount={offlineCount}
              listenerCount={listenerCount}
              meshNodeCount={meshNodeCount}
              useMeshSource={useMeshSource}
              useNetSource={useNetSource}
              netStats={netStats}
              physicsEnabled={physicsEnabled}
              togglePhysics={togglePhysics}
              exportPng={exportPng}
            />
          </div>
        </SheetContent>
      </Sheet>
    </PageContainer>
  );
}
