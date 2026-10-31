import { memo } from "react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { WorkspaceToolbar } from "@/components/ui/workspace-toolbar";
import { CloudUpload, FolderPlus, FolderTree, HardDrive, RotateCw, Search, Usb } from "lucide-react";

type TKey = (key: string, params?: Record<string, string | number>) => string;

interface FilesToolbarProps {
  t: TKey;
  agentId: string;
  onToggleFind: () => void;
  onUpload: () => void;
  onDrives: () => void;
  onMkdir: () => void;
  onUsb: () => void;
  onRefresh: () => void;
}

/** File browser header: title + agent badge + action buttons. */
export default memo(function FilesToolbar({ t, agentId, onToggleFind, onUpload, onDrives, onMkdir, onUsb, onRefresh }: FilesToolbarProps) {
  return (
    <WorkspaceToolbar
      title={t("agents.files_title")}
      icon={FolderTree}
      badges={<Badge variant="secondary" className="font-mono text-xs">{agentId}</Badge>}
      actions={
        <>
          <Button variant="secondary" size="sm" onClick={onToggleFind}>
            <Search className="size-4" />
            {t("agents.files_find")}
          </Button>
          <Button size="sm" onClick={onUpload}>
            <CloudUpload className="size-4" />
            {t("agents.files_upload")}
          </Button>
          <Button variant="secondary" size="sm" onClick={onDrives}>
            <HardDrive className="size-4" />
            {t("agents.files_drives")}
          </Button>
          <Button variant="secondary" size="sm" onClick={onMkdir}>
            <FolderPlus className="size-4" />
            {t("agents.files_mkdir")}
          </Button>
          <Button variant="secondary" size="sm" onClick={onUsb}>
            <Usb className="size-4" />
            {t("agents.files_usb")}
          </Button>
          <Button variant="secondary" size="sm" onClick={onRefresh}>
            <RotateCw className="size-4" />
            {t("agents.files_refresh")}
          </Button>
        </>
      }
    />
  );
});
