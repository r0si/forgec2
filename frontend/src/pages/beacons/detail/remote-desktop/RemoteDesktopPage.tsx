import { useParams } from "react-router-dom";
import { Banner } from "@/components/ui/banner";
import { PageContainer } from "@/components/ui/page-container";
import { TriangleAlert } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { useRemoteDesktop } from "./components/useRemoteDesktop";
import RdpToolbar from "./components/RdpToolbar";
import RdpStage from "./components/RdpStage";
import RdpStatusCards from "./components/RdpStatusCards";

export default function RemoteDesktopPage() {
  const { t } = useI18n();
  const params = useParams();
  const rawId = params.id as string | string[];
  const id = Array.isArray(rawId) ? rawId[0] : rawId || "";

  const rdp = useRemoteDesktop(id);

  return (
    <PageContainer variant="workspace" className="h-full">
      <RdpToolbar
        t={t}
        agentId={id}
        nativeWidth={rdp.liveActive ? rdp.liveWidth : rdp.nativeWidth}
        nativeHeight={rdp.liveActive ? rdp.liveHeight : rdp.nativeHeight}
        resolutions={rdp.RESOLUTIONS}
        resolution={rdp.resolution}
        setResolution={rdp.setResolution}
        mode={rdp.mode}
        setMode={rdp.setMode}
        livePresets={rdp.livePresets}
        livePreset={rdp.livePreset}
        setLivePreset={rdp.setLivePreset}
        active={rdp.sessionActive}
        isFullscreen={rdp.isFullscreen}
        onToggleFullscreen={() => void rdp.toggleFullscreen()}
        versionBlocked={rdp.versionBlocked}
        onStart={rdp.handleStart}
        onStop={rdp.handleStop}
      />

      <div className="flex min-h-0 flex-1 flex-col gap-3 px-(--page-pad-x) py-(--page-pad-y)">
        <Banner tone="warning" icon={<TriangleAlert className="size-4" />} className="items-start">
          <div className="font-semibold">{t("agents.rdp_experimental_title")}</div>
          <div className="text-xs text-muted-foreground">{t("agents.rdp_experimental_desc")}</div>
        </Banner>
        <div className="flex min-h-0 flex-1 flex-col">
          <RdpStage
            t={t}
            status={rdp.status}
            monitoring={rdp.sessionActive}
            live={rdp.liveActive}
            liveMode={rdp.mode === "live"}
            lastUpdate={rdp.lastUpdate}
            wsLive={rdp.wsLive}
            screenData={rdp.screenData}
            liveError={rdp.liveError}
            nativeWidth={rdp.nativeWidth}
            nativeHeight={rdp.nativeHeight}
            mouseX={rdp.mouseX}
            mouseY={rdp.mouseY}
            showCursor={rdp.showCursor}
            containerRef={rdp.containerRef}
            imgRef={rdp.imgRef}
            videoRef={rdp.liveVideoRef}
            onImageLoad={(w, h) => { rdp.setNativeWidth(w); rdp.setNativeHeight(h); }}
            onVideoPlaying={rdp.markLivePlaying}
            onClick={(e) => void rdp.handleClick(e)}
            onMouseMove={rdp.handleMouseMove}
            onMouseLeave={rdp.hideCursor}
          />

          <RdpStatusCards t={t} monitoring={rdp.monitoring} />
        </div>
      </div>
    </PageContainer>
  );
}
