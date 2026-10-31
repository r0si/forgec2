import { memo } from "react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { WorkspaceToolbar } from "@/components/ui/workspace-toolbar";
import { Compass, Expand, Maximize2, Play, Square, Users } from "lucide-react";
import { isExperimentalDesktop } from "../../components/session-quality";
import type { ResolutionOption } from "./useRemoteDesktop";
import type { LivePreset } from "./useWebRTCLive";

type TKey = (key: string, params?: Record<string, string | number>) => string;

interface RdpToolbarProps {
  t: TKey;
  agentId: string;
  nativeWidth: number;
  nativeHeight: number;
  resolutions: ResolutionOption[];
  resolution: string;
  setResolution: (v: string) => void;
  mode: "snapshot" | "live";
  setMode: (mode: "snapshot" | "live") => void;
  livePresets: Array<LivePreset & { label: string }>;
  livePreset: LivePreset["value"];
  setLivePreset: (preset: LivePreset["value"]) => void;
  active: boolean;
  isFullscreen: boolean;
  onToggleFullscreen: () => void;
  versionBlocked: boolean;
  onStart: () => void;
  onStop: () => void;
}

/** Remote-desktop header: title, transport, resolution, fullscreen, start/stop. */
export default memo(function RdpToolbar({
  t, agentId, nativeWidth, nativeHeight, resolutions, resolution, setResolution,
  mode, setMode, livePresets, livePreset, setLivePreset,
  active, isFullscreen, onToggleFullscreen, versionBlocked, onStart, onStop,
}: RdpToolbarProps) {
  return (
    <WorkspaceToolbar
      title={t("agents.rdp_title")}
      icon={Users}
      badges={
        <>
          {isExperimentalDesktop("remote-desktop") ? (
            <span className="text-micro font-normal text-warning-ink">{t("generate.quality_experimental")}</span>
          ) : null}
          <Badge variant="secondary" className="text-xs text-muted-foreground font-mono">
            {agentId}
          </Badge>
          {nativeWidth > 0 && nativeHeight > 0 && (
            <Badge variant="secondary" className="text-xs bg-muted/50 text-muted-foreground flex items-center gap-1">
              <Maximize2 className="size-3.5" />
              {nativeWidth} x {nativeHeight}
            </Badge>
          )}
        </>
      }
      actions={
        <>
          <Select
            value={mode}
            onValueChange={(v) => { if (v === "snapshot" || v === "live") setMode(v); }}
            disabled={active}
          >
            <SelectTrigger className="w-[150px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="snapshot">{t("agents.rdp_mode_snapshot")}</SelectItem>
              <SelectItem value="live">{t("agents.rdp_mode_live")}</SelectItem>
            </SelectContent>
          </Select>
          {mode === "snapshot" ? (
            <Select value={resolution} onValueChange={(v) => { if (v !== null) setResolution(v); }} disabled={active}>
              <SelectTrigger className="w-[180px]">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {resolutions.map((r) => (
                  <SelectItem key={r.value} value={r.value}>
                    {r.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          ) : (
            <Select
              value={livePreset}
              onValueChange={(v) => {
                if (v === "720p" || v === "1080p") setLivePreset(v);
              }}
              disabled={active}
            >
              <SelectTrigger className="w-[180px]">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {livePresets.map((preset) => (
                  <SelectItem key={preset.value} value={preset.value}>
                    {preset.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          <Tooltip>
            <TooltipTrigger render={<Button
                variant="outline"
                size="icon-lg"
                onClick={onToggleFullscreen}
                aria-label={isFullscreen ? t("agents.rdp_exit_fullscreen") : t("agents.rdp_enter_fullscreen")}
              />}>
              {isFullscreen ? <Compass className="size-4" /> : <Expand className="size-4" />}
            </TooltipTrigger>
            <TooltipContent>{isFullscreen ? t("agents.rdp_exit_fullscreen") : t("agents.rdp_fullscreen")}</TooltipContent>
          </Tooltip>
          {!active ? (
            <Button
              onClick={onStart}
              disabled={versionBlocked}
              title={versionBlocked ? t("agents.version_unknown_dest") : undefined}
            >
              <Play className="size-4" />
              {t("agents.rdp_start")}
            </Button>
          ) : (
            <Button variant="destructive" onClick={onStop}>
              <Square className="size-4" />
              {t("agents.rdp_stop")}
            </Button>
          )}
        </>
      }
    />
  );
});
