import { memo, type RefObject } from "react";
import { Card } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { SafeImg } from "@/components/ui/safe-img";
import { Spinner } from "@/components/ui/spinner";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Clock, Keyboard, Mouse, TriangleAlert, Users, Zap } from "lucide-react";
import { safeImageSrc } from "@/lib/safeUrl";
import type { RdpStatus } from "./useRemoteDesktop";

type TKey = (key: string, params?: Record<string, string | number>) => string;

interface RdpStageProps {
  t: TKey;
  status: RdpStatus;
  monitoring: boolean;
  live: boolean;
  liveMode: boolean;
  lastUpdate: string;
  wsLive: boolean;
  screenData: string | null;
  liveError: string;
  nativeWidth: number;
  nativeHeight: number;
  mouseX: number;
  mouseY: number;
  showCursor: boolean;
  containerRef: RefObject<HTMLDivElement | null>;
  imgRef: RefObject<HTMLImageElement | null>;
  videoRef: RefObject<HTMLVideoElement | null>;
  onImageLoad: (w: number, h: number) => void;
  onVideoPlaying: () => void;
  onClick: (e: React.MouseEvent<HTMLDivElement>) => void;
  onMouseMove: (e: React.MouseEvent<HTMLDivElement>) => void;
  onMouseLeave: () => void;
}

/** Interactive remote canvas: status strip, frame/video, cursor, hints, overlay. */
export default memo(function RdpStage({
  t, status, monitoring, live, liveMode, lastUpdate, wsLive, screenData, liveError,
  nativeWidth, nativeHeight, mouseX, mouseY, showCursor,
  containerRef, imgRef, videoRef, onImageLoad, onVideoPlaying, onClick, onMouseMove, onMouseLeave,
}: RdpStageProps) {
  const statusConfig = {
    waiting: {
      color: "bg-muted-foreground dark:bg-muted-foreground",
      text: t("agents.rdp_standby"),
      icon: <span className="size-1.5 rounded-full bg-current" />,
    },
    capturing: {
      color: "bg-warning animate-pulse",
      text: t("agents.rdp_starting"),
      icon: <Spinner size="xs" />,
    },
    connected: {
      color: "bg-success/60 animate-pulse",
      text: t("agents.rdp_connected"),
      icon: <span className="size-1.5 rounded-full bg-current" />,
    },
    error: {
      color: "bg-destructive",
      text: t("agents.rdp_error"),
      icon: <TriangleAlert className="size-3" />,
    },
  };

  const indicator = statusConfig[status];

  return (
    <Card
      className="overflow-hidden shadow-sm flex-1 flex flex-col min-h-0 rounded-lg"
    >
      <div className="bg-muted/50 px-4 py-2.5 flex items-center justify-between border-b border-border shrink-0">
        <div className="flex items-center gap-2">
          <Users className="size-4" />
            <span className="text-sm font-medium text-foreground">
            {t("agents.rdp_interactive")}
          </span>
        </div>
        <div className="flex items-center gap-3 text-xs text-muted-foreground">
          <span
            className={`flex items-center gap-1.5 ${status === "error" ? "text-destructive-ink" : ""}`}
          >
            <span className={`size-2 rounded-full ${indicator.color}`}></span>
            {indicator.icon}
            {indicator.text}
          </span>
            <span className="hidden sm:inline text-muted-foreground">
              <Clock className="size-4" />
            {lastUpdate}
          </span>
          {monitoring && (
            <Tooltip>
              <TooltipTrigger>
                <span
                  className="ml-1 size-2 bg-success rounded-full animate-pulse"
                ></span>
              </TooltipTrigger>
              <TooltipContent>{t("agents.rdp_monitoring_active")}</TooltipContent>
            </Tooltip>
          )}
          {wsLive && (
            <Tooltip>
              <TooltipTrigger>
                <span
                  className="text-micro-sm text-chart-1 flex items-center gap-1"
                >
                  <Zap className="size-4" /> WS
                </span>
              </TooltipTrigger>
              <TooltipContent>{t("agents.rdp_ws_live")}</TooltipContent>
            </Tooltip>
          )}
          {monitoring && (
            <span className="text-micro-sm text-primary flex items-center gap-1">
              <Mouse className="size-4" /> {t("agents.rdp_interactive_label")}
            </span>
          )}
        </div>
      </div>

      <div
        ref={containerRef}
        className="relative bg-card dark:bg-card flex-1 flex items-center justify-center overflow-hidden select-none"
        onClick={onClick}
        onMouseMove={onMouseMove}
        onMouseLeave={onMouseLeave}
      >
        {live ? (
          <>
            <video
              ref={videoRef}
              className="max-w-full max-h-full object-contain bg-black"
              autoPlay
              playsInline
              muted
              onPlaying={onVideoPlaying}
            />
            {monitoring && status !== "error" && (
              <div className="absolute bottom-3 left-3 flex items-center gap-2 text-xs-sm text-white/70 bg-black/50 backdrop-blur-sm px-3 py-1.5 rounded-lg pointer-events-none">
                <Mouse className="size-4" />
                {t("agents.rdp_click_to_interact")}
                <span className="text-white/40 mx-1">|</span>
                <Keyboard className="size-4" />
                {t("agents.rdp_keyboard_active")}
              </div>
            )}
          </>
        ) : liveMode ? (
          <EmptyState icon={Users} title={t("agents.rdp_standby")} message={t("agents.rdp_start_hint")} />
        ) : screenData ? (
          <>
            <SafeImg
              ref={imgRef}
              src={safeImageSrc(screenData)}
              alt={t("nav.beacon_rdp")}
              className="max-w-full max-h-full object-contain"
              draggable={false}
              loading="lazy"
              onLoad={(e) => {
                const img = e.currentTarget;
                onImageLoad(img.naturalWidth || nativeWidth, img.naturalHeight || nativeHeight);
              }}
            />
            {showCursor && monitoring && (
              <div
                className="absolute pointer-events-none"
                style={{
                  left: mouseX,
                  top: mouseY,
                  width: 16,
                  height: 16,
                  transform: "translate(-2px, -2px)",
                }}
              >
                <svg
                  width="16"
                  height="16"
                  viewBox="0 0 16 16"
                  fill="none"
                  className="drop-shadow-lg"
                >
                  <path
                    d="M1 1L6 15L8 9L14 8L1 1Z"
                    fill="white"
                    stroke="black"
                    strokeWidth="1.5"
                    strokeLinejoin="round"
                  />
                </svg>
              </div>
            )}
            {monitoring && (
              <div className="absolute bottom-3 left-3 flex items-center gap-2 text-xs-sm text-white/70 bg-black/50 backdrop-blur-sm px-3 py-1.5 rounded-lg pointer-events-none">
                <Mouse className="size-4" />
                {t("agents.rdp_click_to_interact")}
                <span className="text-white/40 mx-1">|</span>
                <Keyboard className="size-4" />
                {t("agents.rdp_keyboard_active")}
              </div>
            )}
          </>
        ) : (
              <EmptyState icon={Users} title={t("agents.rdp_standby")} message={t("agents.rdp_start_hint")} />
        )}

        {status === "error" && liveError && (liveMode || live) && (
          <div className="absolute inset-x-4 bottom-4 rounded-lg bg-destructive/90 px-3 py-2 text-xs text-white shadow-lg">
            {liveError}
          </div>
        )}

        {status === "capturing" && (
          <div className="absolute inset-0 flex items-center justify-center bg-black/40 backdrop-blur-sm">
            <div className="flex flex-col items-center gap-3">
              <Spinner size="xl" color="white" />
              <span className="text-white text-sm font-medium">{t("agents.rdp_starting_session")}</span>
            </div>
          </div>
        )}
      </div>
    </Card>
  );
});
