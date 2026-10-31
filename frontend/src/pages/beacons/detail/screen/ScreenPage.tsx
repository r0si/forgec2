import { useState } from "react";
import { useParams } from "react-router-dom";
import { PageContainer } from "@/components/ui/page-container";
import { useI18n } from "@/lib/i18n";
import { useScreenMonitor, type ScreenQuality } from "./components/useScreenMonitor";
import ScreenToolbar from "./components/ScreenToolbar";
import ScreenViewer from "./components/ScreenViewer";
import ScreenControls from "./components/ScreenControls";
import ScreenGallery from "./components/ScreenGallery";
import ScreenLightbox from "./components/ScreenLightbox";

export default function ScreenPage() {
  const { t } = useI18n();
  const urlParams = useParams<{ id: string }>();
  const id = Array.isArray(urlParams?.id) ? urlParams.id[0] : urlParams?.id || "";

  const [interval, changeInterval] = useState(3);
  const [quality, setQuality] = useState<ScreenQuality>("medium");
  const [triggerMatch, setTriggerMatch] = useState("");
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [videoMode, setVideoMode] = useState(true);

  const monitor = useScreenMonitor(id, { interval, quality, autoRefresh, t });

  return (
    <PageContainer variant="workspace" className="h-full">
      <div className="flex min-h-0 flex-1 flex-col">
        <ScreenToolbar
          t={t}
          agentId={id}
          resolution={monitor.resolution}
          monitoring={monitor.monitoring}
          busyAction={monitor.busyAction}
          hasScreenshot={monitor.screenshot !== null}
          onStart={() => void monitor.startMonitoring()}
          onStop={() => void monitor.stopMonitoring()}
          onCapture={() => void monitor.requestFreshCapture("capture")}
          onWindowCapture={() => void monitor.requestFreshCapture("window")}
          onDownload={() => monitor.downloadScreenshot()}
        />

        <div className="grid min-h-0 flex-1 grid-cols-1 gap-4 overflow-y-auto px-(--page-pad-x) py-(--page-pad-y) scrollbar-thin lg:grid-cols-[minmax(0,1fr)_19rem] lg:overflow-hidden">
          <ScreenViewer
            t={t}
            screenshot={monitor.screenshot}
            videoMode={videoMode}
            wsLive={monitor.wsLive}
            interval={interval}
            status={monitor.status}
            monitoring={monitor.monitoring}
            monitoringStatus={monitor.monitoringStatus}
            busyAction={monitor.busyAction}
            lastUpdate={monitor.lastUpdate}
            resolution={monitor.resolution}
            onOpenModal={monitor.openModal}
            onActivatePreview={monitor.activatePreview}
          />

          <div className="flex min-h-0 flex-col gap-3 lg:content-auto lg:overscroll-contain lg:overflow-y-auto lg:scrollbar-thin">
            <ScreenControls
              t={t}
              monitoring={monitor.monitoring}
              busyAction={monitor.busyAction}
              interval={interval}
              changeInterval={changeInterval}
              quality={quality}
              setQuality={setQuality}
              autoRefresh={autoRefresh}
              setAutoRefresh={setAutoRefresh}
              videoMode={videoMode}
              setVideoMode={setVideoMode}
              triggerMatch={triggerMatch}
              setTriggerMatch={setTriggerMatch}
              triggerOn={monitor.triggerOn}
              onTriggerStart={() => void monitor.startTitleTrigger(triggerMatch)}
              onTriggerStop={() => void monitor.stopTitleTrigger()}
              resolution={monitor.resolution}
            />

            {!videoMode && (
              <ScreenGallery
                t={t}
                agentId={id}
                gallery={monitor.screenshotGallery}
                onOpen={monitor.openModal}
                onActivate={monitor.activatePreview}
                onDownload={(image, filename) => monitor.downloadScreenshot(image, filename)}
              />
            )}
          </div>
        </div>
      </div>

      <ScreenLightbox
        t={t}
        open={monitor.showModal}
        image={monitor.modalImage}
        onClose={monitor.closeModal}
        onDownload={() => monitor.downloadScreenshot(monitor.modalImage)}
      />
    </PageContainer>
  );
}
