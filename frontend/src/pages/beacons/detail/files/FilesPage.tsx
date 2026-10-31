import { useRef, useState } from "react";
import { useParams } from "react-router-dom";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { PageContainer } from "@/components/ui/page-container";
import { DataError } from "@/components/ui/data-state";
import { Progress } from "@/components/ui/progress";
import { Spinner } from "@/components/ui/spinner";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { FolderOpen, X } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { joinPath, parentPath, type FileEntry } from "@/lib/agent-files/types";
import type { FileSearchHit } from "@/lib/agent-files/file-search";
import { transferPercent } from "@/lib/agent-files/file-task";
import { useAgentFiles } from "./components/useAgentFiles";
import { useFilesView } from "./components/useFilesView";
import FilesToolbar from "./components/FilesToolbar";
import FilesPathBar from "./components/FilesPathBar";
import FilesFindPanel from "./components/FilesFindPanel";
import FilesMediaPanel from "./components/FilesMediaPanel";
import FilesScanPanel from "./components/FilesScanPanel";
import FilesTable from "./components/FilesTable";
import FilesDialogs from "./components/FilesDialogs";

export default function FilesPage() {
  const { t } = useI18n();
  const urlParams = useParams<{ id: string }>();
  const id = Array.isArray(urlParams?.id) ? urlParams.id[0] : urlParams?.id || "";
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [showUpload, setShowUpload] = useState(false);
  const [uploadName, setUploadName] = useState("");
  const [showFind, setShowFind] = useState(false);
  const [view, setView] = useState<"browse" | "media" | "scan">("browse");
  const [showMkdir, setShowMkdir] = useState(false);
  const [mkdirName, setMkdirName] = useState("");
  const [renameTarget, setRenameTarget] = useState<string | null>(null);
  const [renameName, setRenameName] = useState("");
  const [chmodTarget, setChmodTarget] = useState<string | null>(null);
  const [chmodMode, setChmodMode] = useState("644");
  const [dragOver, setDragOver] = useState(false);
  const { confirm, modal } = useConfirm();

  const {
    currentPath,
    currentPathInput,
    setCurrentPathInput,
    entries,
    loading,
    listError,
    selectedFile,
    setSelectedFile,
    uploadProgress,
    uploading,
    pullName,
    pullProgress,
    previewContent,
    previewIsImage,
    showPreview,
    setShowPreview,
    driveOutput,
    showDrives,
    setShowDrives,
    findPattern,
    setFindPattern,
    findResults,
    setFindResults,
    huntDownload,
    setHuntDownload,
    usbOutput,
    showUsb,
    setShowUsb,
    osType,
    osError,
    quickPaths,
    loadDirectory,
    navigateTo,
    downloadFile,
    readFile,
    deleteFile,
    deleteFiles,
    pullFiles,
    batchBusy,
    batchProgress,
    cancelBatch,
    mkdir,
    renameFile,
    chmodFile,
    uploadFile,
    cancelUpload,
    loadDrives,
    loadUsb,
    findFiles,
    huntFiles,
  } = useAgentFiles(id);

  const {
    selected, setSelected,
    sortKey, sortDir, toggleSelect, toggleSort,
    sortedEntries, allSelected,
  } = useFilesView(entries, currentPath);

  const sep = osType === "linux" ? "/" : "\\";

  const handleFileClick = (entry: FileEntry) => {
    if (entry.is_dir) {
      navigateTo(joinPath(currentPath, entry.name, osType));
    } else {
      setSelectedFile(entry.name);
    }
  };

  const handleDelete = async (filename: string) => {
    if (!(await confirm({ message: t("agents.files_confirm_delete") }))) return;
    await deleteFile(filename);
  };

  const handleBatchDelete = async () => {
    const names = [...selected];
    if (names.length === 0) return;
    if (!(await confirm({ message: t("agents.files_confirm_delete") }))) return;
    await deleteFiles(names);
    setSelected(new Set());
  };

  const handleBatchPull = async () => {
    const names = [...selected];
    if (names.length === 0) return;
    await pullFiles(names);
    setSelected(new Set());
  };

  const handleUpload = (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const files = fileInputRef.current?.files;
    if (!files?.length) return;
    // Upload every picked file; the input resets so the same file can retry.
    [...files].forEach((file) => uploadFile(file));
    if (fileInputRef.current) fileInputRef.current.value = "";
    setUploadName("");
    setShowUpload(false);
  };

  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault();
    setDragOver(false);
    const files = e.dataTransfer?.files;
    if (!files?.length) return;
    [...files].forEach((file) => uploadFile(file));
  };

  const handleRevealSearchHit = (hit: FileSearchHit) => {
    navigateTo(parentPath(hit.path, osType));
    setSelectedFile(hit.name);
    setView("browse");
  };

  const handleRevealScanFinding = (path: string, name: string) => {
    navigateTo(parentPath(path, osType));
    setSelectedFile(name);
    setView("browse");
  };

  return (
    <PageContainer variant="workspace" className="h-full">
      {osError && (
        <div className="px-(--page-pad-x) py-(--page-pad-y)">
          <DataError
            message={osError}
            onRetry={() => { window.location.reload(); }}
          />
        </div>
      )}
      {!osError && (
      <>
      <FilesToolbar
        t={t}
        agentId={id}
        onToggleFind={() => { setView("browse"); setShowFind((v) => !v); }}
        onUpload={() => setShowUpload(true)}
        onDrives={() => void loadDrives()}
        onMkdir={() => { setMkdirName(""); setShowMkdir(true); }}
        onUsb={() => void loadUsb()}
        onRefresh={() => void loadDirectory(currentPath, { refresh: true })}
      />

      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-(--page-pad-x) py-(--page-pad-y) scrollbar-thin">
      <p className="text-xs text-muted-foreground">{t("agents.files_channel_hint")}</p>

      <FilesPathBar
        t={t}
        osType={osType}
        currentPath={currentPath}
        currentPathInput={currentPathInput}
        setCurrentPathInput={setCurrentPathInput}
        navigateTo={navigateTo}
        quickPaths={quickPaths}
      />

      <div className="flex flex-wrap items-center gap-2">
        <div className="inline-flex items-center gap-0.5 rounded-lg border border-border/60 bg-muted/65 p-0.5 shadow-sm">
          <Button
            type="button"
            size="sm"
            variant={view === "browse" ? "secondary" : "ghost"}
            aria-pressed={view === "browse"}
            onClick={() => setView("browse")}
          >
            {t("agents.files_tab_browse")}
          </Button>
          <Button
            type="button"
            size="sm"
            variant={view === "media" ? "secondary" : "ghost"}
            aria-pressed={view === "media"}
            onClick={() => { setShowFind(false); setView("media"); }}
          >
            {t("agents.files_tab_media")}
          </Button>
          <Button
            type="button"
            size="sm"
            variant={view === "scan" ? "secondary" : "ghost"}
            aria-pressed={view === "scan"}
            onClick={() => { setShowFind(false); setView("scan"); }}
          >
            {t("agents.files_tab_scan")}
          </Button>
        </div>
      </div>

      {view === "browse" && showFind && (
        <FilesFindPanel
          t={t}
          sep={sep}
          currentPath={currentPath}
          findPattern={findPattern}
          setFindPattern={setFindPattern}
          findResults={findResults}
          setFindResults={setFindResults}
          huntDownload={huntDownload}
          setHuntDownload={setHuntDownload}
          findFiles={() => void findFiles()}
          huntFiles={() => void huntFiles()}
          navigateTo={navigateTo}
          setSelectedFile={setSelectedFile}
        />
      )}

      {view === "media" && (
        <FilesMediaPanel
          t={t}
          agentId={id}
          rootPath={currentPath}
          onReveal={handleRevealSearchHit}
        />
      )}

      {view === "scan" && (
        <FilesScanPanel
          t={t}
          agentId={id}
          rootPath={currentPath}
          onReveal={handleRevealScanFinding}
        />
      )}

      {pullName && pullProgress && (
        <Card className="p-(--card-spacing)">
          <div className="mb-1 flex justify-between text-xs text-muted-foreground">
            <span>{t("agents.files_pulling", { filename: pullName })}</span>
            <span className="font-mono">
              {t("agents.files_pull_progress", {
                current: pullProgress.chunkIndex,
                total: pullProgress.chunkCount,
                pct: transferPercent(pullProgress),
              })}
            </span>
          </div>
          <Progress value={transferPercent(pullProgress)} />
        </Card>
      )}

      {uploading && !showUpload && (
        <Card className="p-(--card-spacing)">
          <div className="mb-1 flex items-center justify-between text-xs text-muted-foreground">
            <span>{t("agents.files_uploading")} {uploadProgress}%</span>
            <Button variant="ghost" size="sm" onClick={cancelUpload}>
              <X className="size-4" /> {t("common.cancel")}
            </Button>
          </div>
          <Progress value={uploadProgress} />
        </Card>
      )}

      {batchBusy && batchProgress && (
        <Card className="p-(--card-spacing)">
          <div className="mb-1 flex items-center justify-between text-xs text-muted-foreground">
            <span className="font-mono">{t("agents.files_selected", { n: `${batchProgress.done}/${batchProgress.total}` })} — {batchProgress.current}</span>
            <Button variant="ghost" size="sm" onClick={cancelBatch}>
              <X className="size-4" /> {t("common.cancel")}
            </Button>
          </div>
          <Progress value={Math.round((batchProgress.done / Math.max(1, batchProgress.total)) * 100)} />
        </Card>
      )}

      {view === "browse" && (
        <>
          {loading && (
            <div className="flex items-center justify-center gap-3 py-10">
              <Spinner size="sm" />
              <p className="text-sm text-muted-foreground">{t("agents.dock_files_waiting")}</p>
            </div>
          )}

          {/* A failed listing stays visible with a retry; the table below (if any)
              holds the last good contents, which may be stale. */}
          {!loading && listError && (
            <DataError
              message={listError}
              onRetry={() => void loadDirectory(currentPath, { refresh: true })}
              className="py-6"
            />
          )}

          {!loading && !listError && entries.length === 0 && (
            <Card className="flex flex-col items-center gap-2 py-10">
              <FolderOpen className="size-6 text-muted-foreground" />
              <p className="text-muted-foreground">{t("agents.files_empty")}</p>
            </Card>
          )}

          {!loading && entries.length > 0 && (
            <FilesTable
              t={t}
              sortedEntries={sortedEntries}
              selected={selected}
              allSelected={allSelected}
              toggleSelect={toggleSelect}
              selectAll={(on) => setSelected(on ? new Set(entries.map((e) => e.name)) : new Set())}
              clearSelection={() => setSelected(new Set())}
              sortKey={sortKey}
              sortDir={sortDir}
              toggleSort={toggleSort}
              selectedFile={selectedFile}
              batchBusy={batchBusy}
              pullName={pullName}
              onFileClick={handleFileClick}
              onPreview={(name) => void readFile(name)}
              onDownload={(name) => void downloadFile(name)}
              onRename={(name) => { setRenameTarget(name); setRenameName(name); }}
              onChmod={(name) => { setChmodTarget(name); setChmodMode("644"); }}
              onDelete={(name) => void handleDelete(name)}
              onBatchPull={() => void handleBatchPull()}
              onBatchDelete={() => void handleBatchDelete()}
              onCancelBatch={cancelBatch}
              dragOver={dragOver}
              onDragOver={() => setDragOver(true)}
              onDragLeave={() => setDragOver(false)}
              onDrop={handleDrop}
            />
          )}
        </>
      )}

      </div>

      <FilesDialogs
        t={t}
        osType={osType}
        currentPath={currentPath}
        showUsb={showUsb}
        setShowUsb={setShowUsb}
        usbOutput={usbOutput}
        showDrives={showDrives}
        setShowDrives={setShowDrives}
        driveOutput={driveOutput}
        showUpload={showUpload}
        setShowUpload={setShowUpload}
        uploading={uploading}
        uploadProgress={uploadProgress}
        uploadName={uploadName}
        setUploadName={setUploadName}
        fileInputRef={fileInputRef}
        onUploadSubmit={handleUpload}
        cancelUpload={cancelUpload}
        showMkdir={showMkdir}
        setShowMkdir={setShowMkdir}
        mkdirName={mkdirName}
        setMkdirName={setMkdirName}
        onMkdirSubmit={(e) => {
          e.preventDefault();
          if (!mkdirName.trim()) return;
          void mkdir(mkdirName).finally(() => setShowMkdir(false));
        }}
        renameTarget={renameTarget}
        setRenameTarget={setRenameTarget}
        renameName={renameName}
        setRenameName={setRenameName}
        chmodTarget={chmodTarget}
        setChmodTarget={setChmodTarget}
        chmodMode={chmodMode}
        setChmodMode={setChmodMode}
        onChmodSubmit={(e) => {
          e.preventDefault();
          if (chmodTarget && chmodMode.trim()) {
            const target = chmodTarget;
            const mode = chmodMode;
            setChmodTarget(null);
            void chmodFile(target, mode);
          }
        }}
        onRenameSubmit={(e) => {
          e.preventDefault();
          if (renameTarget && renameName.trim()) {
            const target = renameTarget;
            const name = renameName;
            setRenameTarget(null);
            void renameFile(target, name);
          }
        }}
        showPreview={showPreview}
        setShowPreview={setShowPreview}
        previewContent={previewContent}
        previewIsImage={previewIsImage}
        selectedFile={selectedFile}
      />
      {modal}
      </>
      )}
    </PageContainer>
  );
}
