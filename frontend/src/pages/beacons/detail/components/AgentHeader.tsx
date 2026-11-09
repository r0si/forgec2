
import { memo, useMemo } from "react";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Link } from "react-router-dom";
import { CopyButton } from "@/components/ui/copy-button";
import { StatusBadge } from "@/components/ui/status-indicator";
import { Spinner } from "@/components/ui/spinner";
import type { AgentDetail, AgentStatus } from "@/types/agent";
import { Activity, Apple, Bot, Camera, Clipboard, Crown, Database, FolderOpen, Key, Keyboard, Link as LinkIcon, ListChecks, MapPin, Monitor, MoreHorizontal, PictureInPicture2, Radio, RefreshCw, Shield, Skull, SlidersHorizontal, Terminal, Trash2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator } from "@/components/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useI18n } from "@/lib/i18n";
import type { ModuleAvailability } from "@/lib/cred-quality";
import { isExperimentalDesktop, sessionActionQuality, type SessionActionQuality } from "./session-quality";
import { implantBlocksDest, knownImplantVersion } from "@/lib/implant-version";
import { rebuildPayloadHref } from "@/lib/generate-query";

function QualityMark({ quality }: { quality: SessionActionQuality }) {
  const { t } = useI18n();
  if (quality === "core") return <>{t("generate.quality_core")}</>;
  if (quality === "hardened") return <>{t("generate.quality_hardened")}</>;
  if (quality === "experimental") return <>{t("generate.quality_experimental")}</>;
  return <>{t("cred.quality_scripted")}</>;
}

function getOSIcon(os: string): React.ReactNode {
  const cls = "size-7 text-primary";
  switch (os.toLowerCase()) {
    case "windows": return <Monitor className={cls} />;
    case "linux": return <Terminal className={cls} />;
    case "darwin": case "macos": return <Apple className={cls} />;
    default: return <Monitor className={cls} />;
  }
}

interface AgentHeaderProps {
  agent: Partial<AgentDetail>;
  agentId: string;
  status: AgentStatus;
  actionLoading: string | null;
  onQuickAction: (action: string, label: string) => void;
  credCount: number | null;
  mimikatzReady?: ModuleAvailability;
  avProducts?: string[];
  avStatus?: "unknown" | "empty" | "products";
  onKill: () => void;
  onUninstall: () => void;
  onMigrate?: () => void;
  onPopOut?: () => void;
}

export default memo(function AgentHeader({
  agent, agentId, status,
  actionLoading, onQuickAction, credCount, mimikatzReady = null, avProducts = [], avStatus = "unknown", onKill, onUninstall, onMigrate, onPopOut,
}: AgentHeaderProps) {
  const { t } = useI18n();
  const hostname = agent.hostname || "\u2014";
  const ip = agent.ip || "\u2014";
  const publicIP = agent.public_ip || "";
  const os = agent.os || "\u2014";
  const arch = agent.arch || "\u2014";
  const username = agent.username || "\u2014";
  const integrity = agent.integrity || "";
  const elevated = Boolean(agent.elevated);
  const domain = agent.domain || "";
  const country = agent.country || "";
  const city = agent.city || "";
  const version = knownImplantVersion(agent.version);

  const quickActions = useMemo(
    () => [
      { action: "screenshot", label: t("agents.header_screenshot"), icon: <Camera className="size-5" /> },
      { action: "ps", label: t("agents.header_processes"), icon: <ListChecks className="size-5" /> },
      { action: "hashdump", label: t("agents.header_hashdump"), icon: <Database className="size-5" />, quality: "scripted" as const },
      { action: "creds_dump", label: t("agents.header_creds_dump"), icon: <Database className="size-5" />, badge: credCount, quality: "scripted" as const },
      { action: "mimikatz", label: t("agents.header_mimikatz"), icon: <Key className="size-5" />, quality: "scripted" as const, needsModule: true },
      { action: "clipboard_get", label: t("agents.header_clipboard"), icon: <Clipboard className="size-5" /> },
      { action: "privesc_check", label: t("agents.header_privesc"), icon: <Shield className="size-5" /> },
      { action: "keylogger_start", label: t("agents.header_key_start"), icon: <Terminal className="size-5" /> },
      { action: "keylogger_stop", label: t("agents.header_key_stop"), icon: <Keyboard className="size-5" /> },
      { action: "keylogger_dump", label: t("agents.header_key_dump"), icon: <Database className="size-5" /> },
    ],
    [t, credCount],
  );

  return (
    <>
      <Card className="mb-(--page-gap) overflow-hidden border-border/70 bg-card/90 shadow-sm">
        <div className="h-1 w-full bg-gradient-to-r from-primary via-chart-2 to-chart-1" />
        <div className="p-(--card-spacing)">
          <div className="flex flex-col gap-4 xl:flex-row xl:items-start xl:justify-between">
            <div className="flex min-w-0 flex-1 items-start gap-4">
              <div
                className="size-14 rounded-2xl bg-primary/10 ring-1 ring-primary/10 flex items-center justify-center shrink-0 select-none shadow-sm"
              >
                {getOSIcon(os)}
              </div>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2 flex-wrap">
                  <h1 className="text-xl font-bold text-foreground truncate">{hostname}</h1>
                  <StatusBadge status={status} pulse={status === "online"} />
                  {integrity && (
                    <Badge variant={integrity.toLowerCase() === "system" || integrity.toLowerCase() === "high" ? "destructive" : integrity.toLowerCase() === "medium" ? "secondary" : "outline"} className="text-micro-sm uppercase">
                      {integrity}
                    </Badge>
                  )}
                  {elevated && (
                    <Badge variant="warning" className="text-micro-sm">
                      <Crown className="size-4" />
                      {t("agents.header_elevated")}
                    </Badge>
                  )}
                  {version ? (
                    <Badge variant="outline" className="font-mono text-micro-sm">{version}</Badge>
                  ) : (
                    <Badge variant="warning" className="text-micro-sm" title={t("agents.version_unknown_hint")} render={<Link to={rebuildPayloadHref(agent)} />}>
                      {t("agents.version_unknown")}
                    </Badge>
                  )}
                </div>
                <div className="mt-2 flex flex-wrap gap-2 text-xs text-muted-foreground">
                  <span className="inline-flex items-center gap-1.5 rounded-full border border-border/70 bg-muted/30 px-2.5 py-1">
                    <span className="text-micro-sm uppercase tracking-wide text-muted-foreground">{t("agents.col_ip")}</span>
                    <span className="font-mono text-foreground">{ip}</span>
                    <CopyButton text={ip} label={t("agents.col_ip")} />
                  </span>
                  {publicIP && (
                    <span className="inline-flex items-center gap-1.5 rounded-full border border-border/70 bg-muted/30 px-2.5 py-1">
                      <span className="text-micro-sm uppercase tracking-wide text-muted-foreground">{t("agents.header_wan")}</span>
                      <span className="font-mono text-foreground">{publicIP}</span>
                      <CopyButton text={publicIP} label={t("agents.stats_public_ip")} />
                    </span>
                  )}
                  <span className="inline-flex items-center gap-1.5 rounded-full border border-border/70 bg-muted/30 px-2.5 py-1">
                    <span className="text-micro-sm uppercase tracking-wide text-muted-foreground">{t("agents.header_user")}</span>
                    <span className="text-foreground">{username}</span>
                  </span>
                  <span className="inline-flex items-center gap-1.5 rounded-full border border-border/70 bg-muted/30 px-2.5 py-1">
                    <span className="text-micro-sm uppercase tracking-wide text-muted-foreground">{t("agents.col_os")}</span>
                    <span className="text-foreground">{os} {arch}</span>
                  </span>
                  {domain && (
                    <span className="inline-flex items-center gap-1.5 rounded-full border border-border/70 bg-muted/30 px-2.5 py-1">
                      <span className="text-micro-sm uppercase tracking-wide text-muted-foreground">{t("agents.header_domain")}</span>
                      <span className="text-foreground">{domain}</span>
                    </span>
                  )}
                  {avProducts.length > 0 ? (
                    <span className="inline-flex items-center gap-1.5 rounded-full border border-success/20 bg-success/10 px-2.5 py-1">
                      <Shield className="size-3 text-success-ink" />
                      <span className="text-micro-sm uppercase tracking-wide text-success-ink">AV</span>
                      <span className="text-foreground">{avProducts.join(", ")}</span>
                    </span>
                  ) : status === "online" && avStatus === "empty" ? (
                    <span className="inline-flex items-center gap-1.5 rounded-full border border-border/70 bg-muted/30 px-2.5 py-1">
                      <Shield className="size-3 text-muted-foreground" />
                      <span className="text-micro-sm uppercase tracking-wide text-muted-foreground">AV</span>
                      <span className="text-muted-foreground text-xs">{t("hostinfo.no_security_products")}</span>
                    </span>
                  ) : status === "online" ? (
                    <span className="inline-flex items-center gap-1.5 rounded-full border border-warning/25 bg-warning/10 px-2.5 py-1">
                      <Shield className="size-3 text-warning-ink" />
                      <span className="text-micro-sm uppercase tracking-wide text-warning-ink">AV</span>
                      <span className="text-warning-ink text-xs">{t("hostinfo.av_status_unknown")}</span>
                    </span>
                  ) : null}
                </div>
                {country && (
                  <p className="mt-2 flex items-center gap-1.5 text-xs text-muted-foreground">
                    <MapPin className="size-4" />
                    {[country, city].filter(Boolean).join(", ")}
                  </p>
                )}
              </div>
            </div>
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4 xl:w-[34rem]">
              <Button variant="outline" size="sm" render={<Link to={`/beacons/${agentId}/shell`} />}><Terminal className="size-4" /> {t("nav.beacon_shell_title")} <kbd className="text-micro-sm opacity-50 ml-1">S</kbd></Button>
              <Button variant="outline" size="sm" render={<Link to={`/ai?agent=${encodeURIComponent(agentId)}`} />}><Bot className="size-4" /> {t("ai.open_for_agent")}</Button>
              <Button variant="outline" size="sm" render={<Link to={rebuildPayloadHref(agent)} />}><RefreshCw className="size-4" /> {t("agents.rebuild_payload")}</Button>
              <Button variant="outline" size="sm" render={<Link to={`/beacons/${agentId}/files`} />}><FolderOpen className="size-4" /> {t("agents.files_title")} <kbd className="text-micro-sm opacity-50 ml-1">F</kbd></Button>
              <Button variant="outline" size="sm" render={<Link to={`/beacons/${agentId}/screen`} />}><Monitor className="size-4" /> {t("nav.beacon_screen")} <span className="text-micro opacity-70"><QualityMark quality="hardened" /></span> <kbd className="text-micro-sm opacity-50 ml-1">D</kbd></Button>
              <Button variant="outline" size="sm" onClick={onPopOut} aria-label={t("agents.popout_console")}>
                <PictureInPicture2 className="size-4" /> {t("agents.popout_console")}
              </Button>
              <DropdownMenu>
                <DropdownMenuTrigger render={<Button variant="secondary" size="sm" className="gap-1.5"><MoreHorizontal className="size-4" /> {t("agents.header_more")}</Button>} />
                <DropdownMenuContent className="w-48">
                  <DropdownMenuItem render={<Link to={`/beacons/${agentId}/token`} />}>
                    <Key className="size-4" /> {t("nav.beacon_token")}
                    <span className="ml-auto text-micro text-muted-foreground"><QualityMark quality="hardened" /></span>
                  </DropdownMenuItem>
                  <DropdownMenuItem render={<Link to={`/beacons/${agentId}/persistence`} />}>
                    <LinkIcon className="size-4" /> {t("nav.beacon_persistence")}
                    <span className="ml-auto text-micro text-muted-foreground"><QualityMark quality="hardened" /></span>
                  </DropdownMenuItem>
                  <DropdownMenuItem render={<Link to={`/beacons/${agentId}/remote-desktop`} />}>
                    <Monitor className="size-4" /> {t("nav.beacon_rdp")}
                    {isExperimentalDesktop("remote-desktop") ? (
                      <span className="ml-auto text-micro text-warning-ink"><QualityMark quality="experimental" /></span>
                    ) : null}
                  </DropdownMenuItem>
                  <DropdownMenuItem onClick={() => onQuickAction("beacon_now", t("agents.beacon_now"))}>
                    <Radio className="size-4" /> {t("agents.beacon_now")}
                  </DropdownMenuItem>
                  <DropdownMenuItem render={<Link to={`/beacons/${agentId}/config`} />}><SlidersHorizontal className="size-4" /> {t("agents.config_hot_config")}</DropdownMenuItem>
                  <DropdownMenuItem render={<Link to={`/beacons/${agentId}/traffic`} />}><Activity className="size-4" /> {t("agents.traffic_title")}</DropdownMenuItem>
                  {onMigrate && (
                    <DropdownMenuItem onClick={onMigrate}><RefreshCw className="size-4" /> {t("agents.header_migrate")}</DropdownMenuItem>
                  )}
                  <DropdownMenuSeparator />
                  <DropdownMenuItem variant="destructive" onClick={onKill}><Skull className="size-4" /> {t("agents.kill_agent")}</DropdownMenuItem>
                  <DropdownMenuItem variant="destructive" onClick={onUninstall}><Trash2 className="size-4" /> {t("agents.uninstall")}</DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          </div>
        </div>
      </Card>

      <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-2 mb-4">
        {quickActions.map((item) => {
          const needsModule = "needsModule" in item && item.needsModule;
          // mimikatzReady === null means the module list could not be read.
          // Stay disabled (never act on unverified state) but do not claim the
          // module is missing.
          const moduleUnknown = !!needsModule && mimikatzReady === null;
          const blocked = !!needsModule && mimikatzReady === false;
          const quality = sessionActionQuality(item.action);
          const versionBlocked = implantBlocksDest(agent.version, quality);
          const disabled = status !== "online" || actionLoading !== null || blocked || moduleUnknown || versionBlocked;
          return (
          <Tooltip key={item.action}>
            <TooltipTrigger render={
              <Button
                variant="ghost"
                disabled={disabled}
                onClick={() => onQuickAction(item.action, item.label)}
                className={`group relative overflow-hidden rounded-lg border border-border/70 bg-card/90 p-3.5 flex flex-col items-center gap-2.5 text-center transition-all duration-200 hover:-translate-y-0.5 hover:shadow-lg hover:border-primary/20 ${disabled ? "opacity-40" : ""}`}
              />
            }>
              <span className="flex size-9 items-center justify-center rounded-full bg-primary/10 text-primary transition-transform group-hover:scale-105">
                {actionLoading === item.action ? <Spinner size="sm" /> : item.icon}
              </span>
              <span className="text-micro-sm font-medium text-foreground leading-tight text-center">{item.label}</span>
              {quality && quality !== "core" && (
                <span className={quality === "scripted" || quality === "experimental" ? "text-micro text-warning-ink" : "text-micro text-muted-foreground"}>
                  <QualityMark quality={quality} />
                </span>
              )}
              {"badge" in item && item.badge != null && (item.badge as number) > 0 && (
                <Badge className="absolute top-1 right-1 min-w-[16px] h-4 px-1 text-micro-sm">{(item.badge as number) > 99 ? "99+" : (item.badge as number)}</Badge>
              )}
            </TooltipTrigger>
            <TooltipContent>
              {moduleUnknown
                ? t("cred.module_status_unknown")
                : blocked
                  ? t("cred.missing_module")
                : versionBlocked
                  ? t("agents.version_unknown_dest")
                : item.action === "privesc_check"
                  ? t("agents.header_privesc_hint")
                  : item.label}
            </TooltipContent>
          </Tooltip>
          );
        })}
      </div>
    </>
  );
})
