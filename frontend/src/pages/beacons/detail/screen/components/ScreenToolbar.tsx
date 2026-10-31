import { memo } from "react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { WorkspaceToolbar } from "@/components/ui/workspace-toolbar";
import { Camera, Download, Maximize2, Monitor, Play, Square } from "lucide-react";
import { Spinner } from "@/components/ui/spinner";
import type { BusyAction } from "./useScreenMonitor";

type TKey = (key: string, params?: Record<string, string | number>) => string;

interface ScreenToolbarProps {
  t: TKey;
  agentId: string;
  resolution: { width: number; height: number } | null;
  monitoring: boolean;
  busyAction: BusyAction;
  hasScreenshot: boolean;
  onStart: () => void;
  onStop: () => void;
  onCapture: () => void;
  onWindowCapture: () => void;
  onDownload: () => void;
}

/** Screen page header: title + monitor/capture actions. */
export default memo(function ScreenToolbar({
  t, agentId, resolution, monitoring, busyAction, hasScreenshot,
  onStart, onStop, onCapture, onWindowCapture, onDownload,
}: ScreenToolbarProps) {
  return (
    <WorkspaceToolbar
      title={t("agents.screen_title")}
      icon={Monitor}
      badges={
        <>
          <Badge variant="secondary" className="max-w-48 truncate font-mono text-xs">{agentId}</Badge>
          {resolution && (
            <Badge variant="outline" className="flex items-center gap-1 text-xs">
              <Maximize2 className="size-3.5" aria-hidden="true" />
              {resolution.width} × {resolution.height}
            </Badge>
          )}
        </>
      }
      actions={
        <>
          {!monitoring ? (
            <Button onClick={onStart} disabled={busyAction !== null}>
              {busyAction === "start" ? <Spinner size="xs" /> : <Play className="size-4" />}
              {t("agents.screen_start")}
            </Button>
          ) : (
            <Button variant="destructive" onClick={onStop} disabled={busyAction !== null}>
              {busyAction === "stop" ? <Spinner size="xs" /> : <Square className="size-4" />}
              {t("agents.screen_stop")}
            </Button>
          )}
          <Button variant="outline" onClick={onCapture} disabled={busyAction !== null}>
            <Camera className="size-4" />
            {t("agents.screen_capture")}
          </Button>
          <Button variant="outline" onClick={onWindowCapture} disabled={busyAction !== null}>
            <Maximize2 className="size-4" />
            {t("agents.screen_window_capture")}
          </Button>
          <Button variant="secondary" onClick={onDownload} disabled={!hasScreenshot}>
            <Download className="size-4" />
            {t("agents.screen_download")}
          </Button>
        </>
      }
    />
  );
});
