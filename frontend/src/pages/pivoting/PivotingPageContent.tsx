
import { registerFragment } from "@/lib/i18n";
import { enPivoting } from "@/lib/i18n/fragments/en.pivoting";
registerFragment(enPivoting);

import { useState, useMemo } from "react";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";
import { EmptyState } from "@/components/ui/empty-state";
import { PageContainer } from "@/components/ui/page-container";
import { Spinner } from "@/components/ui/spinner";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { useErrorToast } from "@/lib/hooks/useErrorToast";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Banner } from "@/components/ui/banner";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { StatusDot } from "@/components/ui/status-dot";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Checkbox } from "@/components/ui/checkbox";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { ArrowDown, ArrowLeftRight, ArrowRight, ArrowUp, Check, Info, Network, Play, PlusCircle, Radio, RotateCw, Route, Square, TriangleAlert, X } from "lucide-react";
import { formatCreated } from "./components/types";
import { formatBytes } from "@/lib/utils";
import { usePivotingData } from "./components/usePivotingData";
import { api } from "@/lib/api";
import { toast } from "sonner";

export default function PivotingPageContent() {
  const { t } = useI18n();
  const { toastError } = useErrorToast();
  const {
    sessions,
    agents,
    loading,
    partialFailures,
    rportForwards,
    loadData,
    startRelay: startRelayApi,
    stopRelay: stopRelayApi,
    startLocalProxy: startLocalProxyApi,
    startRPort: startRPortApi,
    stopRPort: stopRPortApi,
    checkRPortStatus,
  } = usePivotingData();
  const [selectedAgent, setSelectedAgent] = useState("");
  const [relayPort, setRelayPort] = useState(1080);
  const [starting, setStarting] = useState(false);
  const [localStarting, setLocalStarting] = useState(false);
  const [localPort, setLocalPort] = useState(1080);
  const [localAuthEnabled, setLocalAuthEnabled] = useState(false);
  const [localUsername, setLocalUsername] = useState("");
  const [localPassword, setLocalPassword] = useState("");
  const [rportAgent, setRportAgent] = useState("");
  const [rportRemoteHost, setRportRemoteHost] = useState("127.0.0.1");
  const [rportRemotePort, setRportRemotePort] = useState(22);
  const [rportLocalPort, setRportLocalPort] = useState(8022);
  const [rportProtocol, setRportProtocol] = useState<"tcp" | "udp">("tcp");
  const [rportStarting, setRportStarting] = useState(false);
  const [directSocksBusy, setDirectSocksBusy] = useState<string>("");
  const [throughAgent, setThroughAgent] = useState("");
  const [tunAgent, setTunAgent] = useState("");
  const [tunCidr, setTunCidr] = useState("10.66.0.2/24");
  const [tunUdpPort, setTunUdpPort] = useState(0);
  const [tunBusy, setTunBusy] = useState(false);
  const { confirm, modal } = useConfirm();

  const invalidPort = (p: number) => !Number.isInteger(p) || p < 1 || p > 65535;

  const startRelay = async () => {
    if (invalidPort(relayPort)) {
      toast.error(t("pivoting.invalid_port"));
      return;
    }
    setStarting(true);
    await startRelayApi(selectedAgent, relayPort, "127.0.0.1", "socks");
    setStarting(false);
  };

  const startLocalProxy = async () => {
    if (invalidPort(localPort)) {
      toast.error(t("pivoting.invalid_port"));
      return;
    }
    setLocalStarting(true);
    await startLocalProxyApi(
      throughAgent,
      localPort,
      localAuthEnabled ? { username: localUsername, password: localPassword } : undefined,
    );
    setLocalStarting(false);
  };

  const stopRelay = async (agentId: string) => {
    if (!(await confirm({ message: t("pivoting.disconnect_socks") }))) return;
    await stopRelayApi(agentId);
  };

  const startRPort = async () => {
    if (invalidPort(rportRemotePort) || invalidPort(rportLocalPort)) {
      toast.error(t("pivoting.invalid_port"));
      return;
    }
    setRportStarting(true);
    try {
      await startRPortApi({
        rportAgent,
        remoteHost: rportRemoteHost,
        remotePort: rportRemotePort,
        localPort: rportLocalPort,
        protocol: rportProtocol,
      });
    } finally {
      // In-flight guard: double-click fired two POSTs; the second hit the
      // server duplicate rejection and surfaced a spurious error toast.
      setRportStarting(false);
    }
  };

  const stopRPort = async (agentId: string, lport: number) => {
    await stopRPortApi(agentId, lport);
  };

  const maxBytes = useMemo(
    () => sessions.reduce((m, s) => Math.max(m, s.bytes_in || 0, s.bytes_out || 0), 0),
    [sessions],
  );
  const tabOverallMax = Math.max(maxBytes, 1);

  const activeSessions = useMemo(() => sessions.filter(s => s.active), [sessions]);
  const stoppedSessions = useMemo(() => sessions.filter(s => !s.active), [sessions]);

  return (
    <PageContainer title={t("pivoting.title")} icon={<Route className="size-4" />} subtitle={t("pivoting.subtitle")} actions={<>
        <Button variant="outline" onClick={loadData}>
          <RotateCw className="size-4" /> {t("pivoting.refresh")}
        </Button>
      </>}>

      {throughAgent && (
        <Banner tone="info" icon={<Route className="size-4" />} className="animate-fade-in" action={<Button variant="ghost" size="icon" onClick={() => setThroughAgent("")} className="text-info-ink hover:text-primary" aria-label={t("common.clear")}><X className="size-4" /></Button>}>
          {t("pivoting.routing_strip")}: <strong className="font-mono">{throughAgent.substring(0, 12)}</strong>
        </Banner>
      )}

      {!loading && partialFailures.length > 0 && (
        <Banner tone="warning" icon={<TriangleAlert className="size-4" />} className="mb-4 animate-fade-in">
          {t("pivoting.partial_load_failed", { sources: partialFailures.join(", ") })}
        </Banner>
      )}

      <Tabs defaultValue="relay">
        <TabsList sticky>
          {[
            { key: "relay", label: t("pivoting.tab_relay"), Icon: Radio },
            { key: "local", label: t("pivoting.tab_local"), Icon: Network },
            { key: "rport", label: t("pivoting.tab_rport"), Icon: ArrowLeftRight },
            { key: "tun", label: t("pivoting.tab_tun"), Icon: Route },
          ].map((tab) => (
            <TabsTrigger key={tab.key} value={tab.key} className="gap-2">
              <tab.Icon className="size-4" />
              <span>{tab.label}</span>
              {tab.key === "relay" && activeSessions.length > 0 && (
                <StatusDot tone="success" size="sm" pulse />
              )}
            </TabsTrigger>
          ))}
        </TabsList>

      <TabsContent value="relay">
        <>
          <Card className="p-(--card-spacing)">
            <div className="font-semibold text-foreground flex items-center gap-x-2 mb-4">
              <Radio className="size-4" />
              <span>{t("pivoting.sessions_active")}</span>
              <Badge variant="success">{activeSessions.length} running</Badge>
              {stoppedSessions.length > 0 && (
                <Badge variant="secondary">{stoppedSessions.length} stopped</Badge>
              )}
            </div>            {loading ? (
              <div className="text-muted-foreground text-sm py-8 text-center"><Spinner size="sm" /> {t("common.loading")}</div>
            ) : sessions.length === 0 ? (
              <EmptyState icon={Radio} title={t("pivoting.empty_relay_title")} message={t("pivoting.empty_relay_message")} />
            ) : (
              <div className="space-y-3">                {sessions.map((s, i) => (
                  <div key={s.id || i} className={`border rounded-lg p-4 transition-colors ${s.active ? "border-success/30 bg-success/10 dark:border-success/40 dark:bg-success/20" : "border-border opacity-60"}`}>
                    <div className="flex items-center justify-between mb-3">
                      <div className="flex items-center gap-x-3">
                         <StatusDot tone={s.active ? "success" : "muted"} size="md" pulse={s.active} />
                        <div>
                          <div className="font-medium text-sm text-foreground">{String(s.agent_id || "").substring(0, 12)}... {s.hostname && <span className="text-muted-foreground font-normal">({s.hostname})</span>}</div>
                          <div className="text-xs text-muted-foreground">{t("pivoting.port")} {s.listen_port} {s.active ? t("pivoting.running") : t("pivoting.stopped")}  {formatCreated(s.created_at)}</div>
                        </div>
                      </div>
                      <div className="flex items-center gap-x-3">
                        <div className="text-xs text-muted-foreground flex items-center gap-1.5">
                          <Network className="size-4" />
                          {s.active_conn || 0} {t("pivoting.active")} / {s.conn_count || 0} {t("pivoting.total")}                        </div>
                        <Button
                          variant={throughAgent === s.agent_id ? "default" : "outline"}
                          size="sm"
                          onClick={() => {
                            setThroughAgent(s.agent_id);
                             toast.info(t("pivoting.toast.routing_via", { agent_id: String(s.agent_id || "").substring(0, 12) }));
                          }}
                        >
                          {throughAgent === s.agent_id ? <Check className="size-4" /> : <Route className="size-4" />}
                          {throughAgent === s.agent_id ? " Selected" : " Through Me"}
                        </Button>
                        {s.active && (
                          <Button variant="destructive" size="sm" onClick={() => stopRelay(s.agent_id)}>
                            <Square className="size-4" />
                          </Button>
                        )}
                      </div>                    </div>                    <div className="mb-1.5 flex justify-between text-xs text-muted-foreground">
                      <span className="flex items-center gap-1"><ArrowDown className="size-4" />{formatBytes(s.bytes_in || 0)} down</span>
                      <span className="flex items-center gap-1"><ArrowUp className="size-4" />{formatBytes(s.bytes_out || 0)} up</span>
                    </div>
                    <div className="flex h-2 bg-secondary rounded-full overflow-hidden">
                      <div className="bg-success transition-all" style={{ width: `${tabOverallMax > 0 ? ((s.bytes_in || 0) / tabOverallMax) * 100 : 0}%` }}></div>
                      <div className="bg-info transition-all" style={{ width: `${tabOverallMax > 0 ? ((s.bytes_out || 0) / tabOverallMax) * 100 : 0}%` }}></div>
                    </div>                  </div>
                ))}
              </div>            )}
          </Card>

          <Card className="p-(--card-spacing)">
            <div className="font-semibold text-foreground flex items-center gap-x-2 mb-4">
              <PlusCircle className="size-4" /> Start New SOCKS Relay
            </div>
            <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
              <div>
                <Label className="block mb-1.5">{t("pivoting.target_agent")}</Label>
                <Select value={selectedAgent} onValueChange={(v) => setSelectedAgent(v ?? "")}>
                  <SelectTrigger className="w-full"><SelectValue placeholder={t("pivoting.select_agent")} /></SelectTrigger>
                  <SelectContent>
                    {agents.map(a => (
                      <SelectItem key={a.id} value={a.id}>{a.hostname} ({a.ip})</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>              <div>
                <Label className="block mb-1.5">{t("pivoting.listen_port")}</Label>
                <Input aria-label={t("pivoting.socks_listen_port")} name="input-2" type="number" value={relayPort} onChange={e => setRelayPort(Number(e.target.value))} min={1} max={65535} />
              </div>
            </div>
            <div className="mt-4 flex items-center gap-3">
              <Button onClick={startRelay} size="lg" disabled={!selectedAgent || starting}
                className="px-6 disabled:opacity-50 disabled:cursor-not-allowed text-sm font-medium whitespace-nowrap transition-colors flex items-center justify-center gap-2">
                {starting ? <><Spinner size="xs" /> {t("pivoting.starting")}</> : <><Play className="size-4" /> {t("pivoting.start")} SOCKS5 Relay</>}
              </Button>
            </div>
            <div className="mt-3 p-3 bg-muted rounded-lg text-xs text-muted-foreground flex items-start gap-1.5">
              <Info className="size-4" />
              <span>{t("pivoting.proxy_hint")}</span>
            </div>          </Card>
        </>
      </TabsContent>

      <TabsContent value="local">
        <>          <Card className="p-(--card-spacing)">
            <div className="font-semibold text-foreground flex items-center gap-x-2 mb-4">
              <Network className="size-4" />
              <span>{t("pivoting.local_proxy")}</span>
            </div>
            <div className="grid grid-cols-1 md:grid-cols-3 gap-4">              <div>
                <Label className="block mb-1.5">{t("pivoting.proxy_port")}</Label>
                <Input aria-label={t("pivoting.local_proxy_port")} name="input-4" type="number" value={localPort} onChange={e => setLocalPort(Number(e.target.value))} min={1} max={65535} />
              </div>
              <div>
                <Label className="block mb-1.5">{t("pivoting.route_through")}</Label>
                <Select value={throughAgent} onValueChange={(v) => setThroughAgent(v ?? "")}>
                  <SelectTrigger className="w-full"><SelectValue placeholder="-- Direct (no pivot) --" /></SelectTrigger>
                  <SelectContent>
                    {sessions.filter(s => s.active).map(s => (
                      <SelectItem key={s.agent_id} value={s.agent_id}>{String(s.agent_id || "").substring(0, 12)}... (:{s.listen_port})</SelectItem>
                    ))}
                    {agents.map(a => (
                      <SelectItem key={a.id} value={a.id}>{a.hostname} ({a.ip})</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div>                <Label className="block mb-1.5">{t("pivoting.auth_label")}</Label>
                <div className="flex items-center gap-3 mt-2">                  <Label className="flex items-center gap-2 cursor-pointer">
                    <Checkbox checked={localAuthEnabled} onCheckedChange={setLocalAuthEnabled} />                    <span className="text-sm text-muted-foreground">{t("pivoting.enable")}</span>
                  </Label>
                </div>
              </div>
            </div>            {localAuthEnabled && (
              <div className="mt-4 grid grid-cols-1 md:grid-cols-2 gap-4">
                <div>                  <Label className="block mb-1.5">{t("pivoting.username_label")}</Label>
                  <Input aria-label={t("pivoting.operator_label")} name="input-7" type="text" value={localUsername} onChange={e => setLocalUsername(e.target.value)} placeholder="operator" />
                </div>                <div>
                  <Label className="block mb-1.5">{t("pivoting.password")}</Label>
                  <Input aria-label={t("pivoting.password")} name="input-8" type="password" value={localPassword} onChange={e => setLocalPassword(e.target.value)} placeholder={t("pivoting.password")} />                </div>
              </div>
            )}
            <div className="mt-4 flex items-center gap-3">
              <Button onClick={startLocalProxy} disabled={localStarting} className="gap-2">
                <Play className="size-4" />
                {localStarting ? t("pivoting.starting") : t("pivoting.start_local_proxy")}
              </Button>
              <Button variant="outline" onClick={() => { setLocalPort(1080); setThroughAgent(""); setLocalAuthEnabled(false); setLocalUsername(""); setLocalPassword(""); }}>
                {t("pivoting.reset")}
              </Button>
            </div>
          </Card>          <Card className="p-(--card-spacing)">
            <div className="font-semibold text-foreground flex items-center gap-x-2 mb-3">
              <Network className="size-4" /> {t("pivoting.direct_socks")}            </div>
            <p className="text-sm text-muted-foreground mb-4">
              {t("pivoting.direct_socks_desc")}
            </p>
            {loading ? (
              <div className="flex items-center justify-center py-8">
                <Spinner size="sm" />
              </div>
            ) : agents.length > 0 ? (              <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
                {agents.map(a => (                  <Card key={a.id} className="hover:border-warning/40 dark:hover:border-warning hover:shadow-sm transition-all">
                    <CardContent className="p-4">
                      <div className="flex justify-between items-center mb-1">
                        <div className="font-medium text-foreground">{a.hostname}</div>
                        <span className="text-xs text-muted-foreground">{a.ip}</span>
                      </div>
                      <div className="text-xs text-muted-foreground mb-3 font-mono">{a.id.substring(0, 12)}...</div>
                      <Button variant="secondary" size="sm" disabled={directSocksBusy === a.id || invalidPort(localPort)} onClick={async () => {
                        if (invalidPort(localPort)) {
                          // Port field may be cleared: never POST port "0".
                          toast.error(t("pivoting.invalid_port"));
                          return;
                        }
                        setDirectSocksBusy(a.id);
                        try {
                          await api.post(paths.agents.socks(a.id), { port: localPort.toString() });
                          toast.success(t("pivoting.toast.direct_socks", { host: a.hostname, port: String(localPort) }));
                        } catch (err) {
                          toastError(err, "pivoting.toast.direct_socks_failed");
                        } finally {
                          setDirectSocksBusy("");
                        }
                      }}
                        className="w-full text-xs px-3 py-2 transition-colors flex items-center justify-center gap-1.5">
                        <Play className="size-4" /> {t("pivoting.start_socks_on", { port: String(localPort) })}
                      </Button>
                    </CardContent>
                  </Card>
                ))}              </div>
            ) : (
              <div className="text-muted-foreground text-sm py-4 text-center">{t("pivoting.no_agents")}</div>            )}
          </Card>
        </>
      </TabsContent>

      <TabsContent value="rport">
        <>
          <Card className="p-(--card-spacing)">
            <div className="font-semibold text-foreground flex items-center gap-x-2 mb-4">              <ArrowLeftRight className="size-4" />
              <span>{t("pivoting.active_reverse_forwards")}</span>
              <Badge variant="secondary">{rportForwards.filter(r => r.active).length} {t("pivoting.active")}</Badge>
            </div>
            {loading ? (
              <div className="flex items-center justify-center py-8">
                <Spinner size="sm" />
              </div>
            ) : rportForwards.length === 0 ? (
              <EmptyState icon={ArrowLeftRight} title={t("pivoting.empty_rportfwd_title")} />
            ) : (
              <div className="space-y-3">
                {rportForwards.map(rf => (
                  <div key={`${rf.agent_id}:${rf.local_port}`} className={`rounded-lg p-4 transition-colors border ${rf.active ? "border-info/30 bg-info/10 dark:border-info/40 dark:bg-info/20" : "border-border opacity-60"}`}>
                    <div className="flex items-center justify-between mb-3">
                      <div className="flex items-center gap-x-3">                        <StatusDot tone={rf.active ? "info" : "muted"} size="md" pulse={rf.active} />
                        <div>                          <div className="text-sm font-medium text-foreground flex items-center gap-2">                            <span className="font-mono">{rf.remote_host}:{rf.remote_port}</span>
                            <ArrowRight className="size-4" />
                            <span className="font-mono">localhost:{rf.local_port}</span>
                            <Badge variant="outline">{rf.protocol.toUpperCase()}</Badge>
                          </div>
                          <div className="text-xs text-muted-foreground mt-0.5">{t("pivoting.agent")}: {String(rf.agent_id || "").substring(0, 12)}... {rf.active ? t("pivoting.active") : t("pivoting.stopped")}</div>
                        </div>
                      </div>
                      <div className="flex items-center gap-2">
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => checkRPortStatus(rf.agent_id)}
                        >
                          <Info className="size-4" /> Status
                        </Button>
                        <Button
                          variant="destructive"
                          size="sm"
                          onClick={() => stopRPort(rf.agent_id, rf.local_port)}
                        >                          <Square className="size-4" /> Stop
                        </Button>
                      </div>                    </div>
                  </div>
                ))}
              </div>            )}
          </Card>

          <Card className="p-(--card-spacing)">
            <div className="font-semibold text-foreground flex items-center gap-x-2 mb-4">              <PlusCircle className="size-4" /> New Reverse Port Forward
            </div>            <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mb-4">
              <div>                <Label className="block mb-1.5">{t("pivoting.agent")}</Label>
                <Select value={rportAgent} onValueChange={(v) => setRportAgent(v ?? "")}>
                  <SelectTrigger className="w-full"><SelectValue placeholder={t("pivoting.select_agent")} /></SelectTrigger>
                  <SelectContent>
                    {agents.map(a => (                    <SelectItem key={a.id} value={a.id}>{a.hostname} ({a.ip})</SelectItem>
                    ))}
                  </SelectContent>
                </Select>              </div>
              <div>
                <Label className="block mb-1.5">{t("pivoting.remote_host")}</Label>                <Input aria-label={t("pivoting.remote_host")} name="input-10" type="text" value={rportRemoteHost} onChange={e => setRportRemoteHost(e.target.value)} placeholder="127.0.0.1" />
              </div>              <div>                <Label className="block mb-1.5">{t("pivoting.remote_port")}</Label>
                <Input aria-label={t("pivoting.remote_port")} name="input-11" type="number" value={rportRemotePort} onChange={e => setRportRemotePort(Number(e.target.value))} min={1} max={65535} />              </div>
              <div>
                <Label className="block mb-1.5">{t("pivoting.local_forward_port")}</Label>                <Input aria-label={t("pivoting.local_forward_port")} name="input-12" type="number" value={rportLocalPort} onChange={e => setRportLocalPort(Number(e.target.value))} min={1} max={65535} />              </div>
            </div>            <div className="flex items-center gap-3">
              <div>
                <Label className="block mb-1.5">{t("pivoting.protocol")}</Label>
                {/* Server-side relay is TCP-only (net.Listen "tcp"); UDP here
                    silently created a TCP tunnel — lock the selector to TCP. */}
                <Select value={rportProtocol} onValueChange={v => setRportProtocol(v as "tcp" | "udp")} disabled>
                  <SelectTrigger className="w-full"><SelectValue placeholder={t("pivoting.protocol_ph")} /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="tcp">TCP</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <Button onClick={startRPort} size="lg" disabled={!rportAgent || rportStarting}
                className="px-6 bg-primary hover:bg-primary/90 text-primary-foreground disabled:opacity-50 disabled:cursor-not-allowed text-sm font-medium transition-colors flex items-center gap-2">
                <Play className="size-4" /> {t("pivoting.start_forward")}</Button>
              <Button variant="outline" size="lg"
                onClick={() => {
                  toast.info(t("pivoting.toast.refreshing_rport"));
                  void loadData();
                }}
                className="px-4 flex items-center gap-1.5"
              >
                <RotateCw className="size-4" /> {t("pivoting.refresh_status")}
              </Button>
            </div>
            <Banner tone="info" icon={<Info className="size-4" />} className="items-start">
              {t("pivoting.rport_hint", { local: rportLocalPort, host: rportRemoteHost || "[target]", remote: rportRemotePort })}
            </Banner>
          </Card>        </>
      </TabsContent>

      <TabsContent value="tun">
        <Card className="p-(--card-spacing) space-y-4">
          <div className="font-semibold">{t("pivoting.tun_title")}</div>
          <p className="text-xs text-muted-foreground">{t("pivoting.tun_hint")}</p>
          <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
            <div>
              <Label className="block mb-1.5">{t("pivoting.target_agent")}</Label>
              <Select value={tunAgent} onValueChange={(v) => setTunAgent(v ?? "")}>
                <SelectTrigger className="w-full"><SelectValue placeholder={t("pivoting.agent")} /></SelectTrigger>
                <SelectContent>
                  {agents.map(a => (
                    <SelectItem key={a.id} value={a.id}>{a.hostname} ({a.ip})</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div>
              <Label className="block mb-1.5">{t("pivoting.tun_cidr")}</Label>
              <Input value={tunCidr} onChange={(e) => setTunCidr(e.target.value)} />
            </div>
            <div>
              <Label className="block mb-1.5">{t("pivoting.tun_udp_port")}</Label>
              <Input type="number" value={tunUdpPort} onChange={(e) => setTunUdpPort(Number(e.target.value))} min={0} max={65535} />
            </div>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              disabled={!tunAgent || tunBusy}
              onClick={async () => {
                // Backend accepts 0 (auto) and rejects only negatives — the
                // shared invalidPort() here blocked the untouched default.
                if (!Number.isInteger(tunUdpPort) || tunUdpPort < 0 || tunUdpPort > 65535) {
                  toast.error(t("pivoting.invalid_port"));
                  return;
                }
                setTunBusy(true);
                try {
                  // Handler reads PostForm — must send urlencoded, not JSON
                  // (a JSON body silently ignored cidr/udp_port defaults).
                  const res = await api.post<{ udp_port?: number }>(paths.agents.tunStart(tunAgent), {
                    cidr: tunCidr,
                    udp_port: String(tunUdpPort || 0),
                  });
                  toast.success(t("pivoting.tun_started", { port: String(res.udp_port ?? 0) }));
                } catch {
                  toast.error(t("pivoting.tun_failed"));
                } finally {
                  setTunBusy(false);
                }
              }}
            >
              <Play className="size-4" /> {t("pivoting.tun_start")}
            </Button>
            <Button
              variant="outline"
              disabled={!tunAgent || tunBusy}
              onClick={async () => {
                setTunBusy(true);
                try {
                  await api.post(paths.agents.tunStop(tunAgent), {});
                  toast.success(t("pivoting.tun_stopped"));
                } catch {
                  toast.error(t("pivoting.tun_failed"));
                } finally {
                  setTunBusy(false);
                }
              }}
            >
              <Square className="size-4" /> {t("pivoting.tun_stop")}
            </Button>
          </div>
        </Card>
      </TabsContent>
      </Tabs>
      {modal}
    </PageContainer>
  );
}

