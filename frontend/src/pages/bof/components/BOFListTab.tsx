
import { useState } from "react";
import type { BOFFile } from "./types";
import { formatBytes } from "@/lib/utils";
import { Spinner } from "@/components/ui/spinner";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { EmptyState } from "@/components/ui/empty-state";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog";
import { Box, Info, Pencil, Play, Trash2, Upload } from "lucide-react";
import { useI18n } from "@/lib/i18n";

interface BOFListTabProps {
  files: BOFFile[];
  loading: boolean;
  onUpload: (file: File, arch: string, name: string, desc: string) => void;
  onDelete: (id: number) => void;
  onRun: (id: number, agentId: string, args: string) => void;
  onEdit: (id: string, name: string, description: string) => void;
  agents: Array<{ id: string; hostname: string }>;
}

export default function BOFListTab({ files, loading, onUpload, onDelete, onRun, onEdit, agents }: BOFListTabProps) {
  const { t } = useI18n();
  const [showUpload, setShowUpload] = useState(false);
  const [showRun, setShowRun] = useState(false);
  const [showInfo, setShowInfo] = useState<BOFFile | null>(null);
  const [editTarget, setEditTarget] = useState<BOFFile | null>(null);
  const [uploadName, setUploadName] = useState("");
  const [uploadDesc, setUploadDesc] = useState("");
  const [uploadArch, setUploadArch] = useState("x64");
  const [uploadFile, setUploadFile] = useState<File | null>(null);
  const [runBofId, setRunBofId] = useState<number>(0);
  const [runAgent, setRunAgent] = useState("");
  const [runArgs, setRunArgs] = useState("");

  const handleUpload = (e: React.FormEvent) => {
    e.preventDefault();
    if (!uploadFile) return;
    onUpload(uploadFile, uploadArch, uploadName, uploadDesc);
    setShowUpload(false);
    setUploadFile(null);
    setUploadName("");
    setUploadDesc("");
    setUploadArch("x64");
  };

  const handleRun = (e: React.FormEvent) => {
    e.preventDefault();
    if (!runAgent) return;
    onRun(runBofId, runAgent, runArgs);
    setShowRun(false);
  };

  const handleEdit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!editTarget) return;
    onEdit(String(editTarget.id || ""), String(editTarget.name || ""), String(editTarget.description || ""));
    setEditTarget(null);
  };

  if (loading) {
    return (
      <Card className="overflow-hidden">
        <div className="text-center py-12 text-muted-foreground">
          <Spinner />
        </div>
      </Card>
    );
  }

  return (
    <>
      <div className="flex justify-end mb-4">
        <Button onClick={() => setShowUpload(true)}>
          <Upload className="size-4" />
          {t("bof.upload_title")}
        </Button>
      </div>

      <Card className="overflow-hidden">
        {files.length > 0 ? (
          <div className="divide-y divide-border">
            {files.map((b: BOFFile, i: number) => {
              const bid = b.id || String(i);
              const bname = b.name || t("bof.unknown");
              const bdesc = b.description || "";
              const bsize = formatBytes(b.size);
              const barch = b.architecture || "x64";
              return (
                <div key={bid} className="px-5 py-4 hover:bg-muted transition-colors">
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-3 flex-1 min-w-0">
                      <div className="size-8 bg-primary/10 dark:bg-primary/15 rounded-lg flex items-center justify-center text-primary text-xs font-bold shrink-0">
                        <Box className="size-4" />
                      </div>
                      <div className="min-w-0">
                        <div className="text-sm font-medium text-foreground truncate">{bname}</div>
                        <div className="text-xs text-muted-foreground mt-0.5">
                          {bsize} · {barch} {bdesc ? `· ${bdesc}` : ""}
                        </div>
                      </div>
                    </div>
                    <div className="flex gap-2 ml-4">
                      <Button
                        onClick={() => {
                          setRunBofId(Number(bid) || 0);
                          setRunAgent(agents[0]?.id || "");
                          setRunArgs("");
                          setShowRun(true);
                        }}
                        className="px-3 py-1.5 text-xs bg-success/15 text-success-ink rounded-lg border border-success/30 hover:bg-success/15 transition-colors"
                      >
                        <Play className="size-4" />{t("bof.run")}
                      </Button>
                      <Button
                        onClick={() => setShowInfo(b)}
                        aria-label={t("bof.info_title")}
                        className="px-3 py-1.5 text-xs bg-secondary text-muted-foreground rounded-lg border border-border hover:bg-secondary transition-colors"
                      >
                        <Info className="size-4" />
                      </Button>
                      <Button
                        onClick={() => setEditTarget(b)}
                        aria-label={t("bof.edit_title")}
                        className="px-3 py-1.5 text-xs bg-secondary text-muted-foreground rounded-lg border border-border hover:bg-secondary transition-colors"
                      >
                        <Pencil className="size-4" />
                      </Button>
                      <Button
                        onClick={() => onDelete(Number(bid) || 0)}
                        aria-label={t("common.delete")}
                        className="px-3 py-1.5 text-xs bg-destructive/10 text-destructive-ink rounded-lg border border-destructive/20 hover:bg-destructive/10 transition-colors"
                      >
                        <Trash2 className="size-4" />
                      </Button>
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        ) : (
          <EmptyState icon={Box} title={t("bof.no_files")} message={t("bof.no_files_hint")} />
        )}
      </Card>

      <Dialog open={showUpload} onOpenChange={setShowUpload}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t("bof.upload_title")}</DialogTitle>
          </DialogHeader>
          <form onSubmit={handleUpload} className="space-y-4">
            <div>
              <span className="block text-xs font-semibold text-muted-foreground mb-1.5">{t("bof.bof_file")} (.o)</span>
              <Input
                type="file"
                accept=".o"
                onChange={(e) => setUploadFile(e.target.files?.[0] || null)}
                required
                aria-label={t("bof.bof_file")}
                className="h-9 bg-muted"
              />
            </div>
            <div>
              <span className="block text-xs font-semibold text-muted-foreground mb-1.5">{t("bof.name")}</span>
              <Input
                aria-label={t("bof.bof_name")}
                placeholder={t("bof.bof_name")}
                required
                value={uploadName}
                onChange={(e) => setUploadName(e.target.value)}
                className="w-full h-9"
              />
            </div>
            <div>
              <span className="block text-xs font-semibold text-muted-foreground mb-1.5">{t("bof.description")}</span>
              <Input
                aria-label={t("bof.description")}
                placeholder={t("bof.brief_desc")}
                value={uploadDesc}
                onChange={(e) => setUploadDesc(e.target.value)}
                className="w-full h-9"
              />
            </div>
            <div>
              <span className="block text-xs font-semibold text-muted-foreground mb-1.5">{t("bof.architecture")}</span>
              <Select value={uploadArch} onValueChange={(v) => setUploadArch(v ?? "")}>
                <SelectTrigger aria-label={t("bof.architecture")} className="w-full h-9">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="x64">x64</SelectItem>
                  <SelectItem value="x86">x86</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <DialogFooter>
              <Button type="submit" size="lg" className="w-full text-sm font-medium transition-colors">
                {t("bof.upload_title")}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={showRun} onOpenChange={setShowRun}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t("bof.execute_title")}</DialogTitle>
          </DialogHeader>
          <form onSubmit={handleRun} className="space-y-4">
            <div>
              <span className="block text-xs font-semibold text-muted-foreground mb-1.5">{t("bof.agent")}</span>
              <Select value={runAgent} onValueChange={(v) => setRunAgent(v ?? "")}>
                <SelectTrigger aria-label={t("bof.agent")} className="w-full h-9">
                  <SelectValue placeholder={t("bof.select_agent")} />
                </SelectTrigger>
                <SelectContent>
                  {agents.map((a: { id: string; hostname: string }) => (
                    <SelectItem key={a.id} value={a.id}>
                      {a.hostname}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div>
              <span className="block text-xs font-semibold text-muted-foreground mb-1.5">{t("bof.arguments")}</span>
              <Input aria-label={t("bof.bof_args")} placeholder={t("bof.bof_args")} value={runArgs} onChange={(e) => setRunArgs(e.target.value)} className="w-full h-9 font-mono" />
            </div>
            <DialogFooter>
              <Button type="submit" size="lg" className="w-full text-sm font-medium transition-colors">
                {t("bof.run")}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <Dialog open={!!showInfo} onOpenChange={() => setShowInfo(null)}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t("bof.info_title")}</DialogTitle>
          </DialogHeader>
          {showInfo && (
            <div className="space-y-3">
              <div className="flex justify-between py-2 border-b border-border">
                <span className="text-sm text-muted-foreground">{t("bof.name")}</span>
                <span className="text-sm font-medium text-foreground font-mono">{showInfo.name}</span>
              </div>
              <div className="flex justify-between py-2 border-b border-border">
                <span className="text-sm text-muted-foreground">{t("bof.size")}</span>
                <span className="text-sm text-foreground">{formatBytes(showInfo.size)}</span>
              </div>
              <div className="flex justify-between py-2 border-b border-border">
                <span className="text-sm text-muted-foreground">{t("bof.architecture")}</span>
                <span className="text-sm text-foreground">{showInfo.architecture || "x64"}</span>
              </div>
              <div className="flex justify-between py-2 border-b border-border">
                <span className="text-sm text-muted-foreground">{t("bof.description")}</span>
                <span className="text-sm text-foreground max-w-[60%] text-right">{showInfo.description || "-"}</span>
              </div>
              <div className="flex justify-between py-2 border-b border-border">
                <span className="text-sm text-muted-foreground">{t("bof.uploaded")}</span>
                <span className="text-sm text-foreground">{showInfo.created_at || "-"}</span>
              </div>
              <div className="flex justify-between py-2">
                <span className="text-sm text-muted-foreground">{t("groups.created_by")}</span>
                <span className="text-sm text-foreground">{showInfo.created_by || showInfo.CreatedBy || "-"}</span>
              </div>
            </div>
          )}
        </DialogContent>
      </Dialog>

      <Dialog open={!!editTarget} onOpenChange={() => setEditTarget(null)}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t("bof.edit_title")}</DialogTitle>
          </DialogHeader>
          {editTarget && (
            <form onSubmit={handleEdit} className="space-y-4">
              <div>
                <span className="block text-xs font-semibold text-muted-foreground mb-1.5">{t("bof.name")}</span>
                <Input aria-label={t("bof.bof_name")} value={editTarget.name || ""} onChange={(e) => setEditTarget({ ...editTarget, name: e.target.value })} className="w-full h-9 font-mono" />
              </div>
              <div>
                <span className="block text-xs font-semibold text-muted-foreground mb-1.5">{t("bof.description")}</span>
                <Input aria-label={t("bof.description")} value={editTarget.description || ""} onChange={(e) => setEditTarget({ ...editTarget, description: e.target.value })} className="w-full h-9" />
              </div>
              <DialogFooter>
                <Button type="submit" size="lg" className="w-full text-sm font-medium transition-colors">
                  {t("common.save")}
                </Button>
              </DialogFooter>
            </form>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}

