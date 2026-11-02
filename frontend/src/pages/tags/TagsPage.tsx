
import { useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useApiResource } from "@/lib/hooks/useApiResource";
import { POLL } from "@/lib/polling";
import { useI18n } from "@/lib/i18n";
import { PageContainer } from "@/components/ui/page-container";
import { ConfirmModal } from "@/components/ui/confirm-modal";
import { DataState } from "@/components/ui/data-state";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { formatTime } from "@/lib/utils";
import { Pencil, Plus, Tag, Trash2, X } from "lucide-react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { usePermissions } from "@/lib/hooks/usePermissions";

interface Tag {
  id: string;
  name: string;
  color: string;
  agent_count: number;
  created_at: string;
  updated_at: string;
}

async function fetchTags() {
  return api.get(paths.tags.list);
}

const TAG_COLORS = [
  "#3498db", "#2ecc71", "#e74c3c", "#f39c12", "#9b59b6",
  "#1abc9c", "#e67e22", "#34495e", "#16a085", "#c0392b",
  "#2980b9", "#27ae60", "#8e44ad", "#d35400", "#7f8c8d",
];

export default function TagsPage() {
  const { t } = useI18n();
  const { can } = usePermissions();
  // Reads live under agents.read; the mutations below still need
  // agents.write, so read-only roles see the inventory without dead buttons.
  const canWrite = can("agents.write");
  const { data, loading, error, refresh: loadTags } = useApiResource<Tag[]>({
    fetcher: async () => {
      const data = await fetchTags();
      return (Array.isArray(data) ? data : data.tags || []) as Tag[];
    },
    toastThrottleMs: POLL.toastThrottle,
    errorMessage: t("tags.toast.load_failed"),
  });
  const tags = data ?? [];
  const [modal, setModal] = useState<{ mode: "create" | "edit"; tag?: Tag } | null>(null);
  const [deleteConfirm, setDeleteConfirm] = useState<Tag | null>(null);
  const [actionMsg, setActionMsg] = useState<string | null>(null);
  const [formName, setFormName] = useState("");
  const [formColor, setFormColor] = useState("#3498db");

  const openCreate = () => {
    setFormName("");
    setFormColor(TAG_COLORS[0]);
    setModal({ mode: "create" });
  };

  const openEdit = (tag: Tag) => {
    setFormName(tag.name);
    setFormColor(tag.color);
    setModal({ mode: "edit", tag });
  };

  const handleSave = async () => {
    if (!formName.trim()) return;
    try {
      if (modal?.mode === "create") {
        const data = await api.postJson<{ success?: boolean; tag?: Tag; error?: string }>(paths.tags.list, { name: formName.trim(), color: formColor });
        if (data.success) {
          setActionMsg(t("tags.toast.created"));
          setModal(null);
          loadTags();
        } else {
          setActionMsg(data.error || t("tags.toast.create_failed"));
        }
      } else if (modal?.mode === "edit" && modal.tag) {
        const data = await api.put<{ success?: boolean; error?: string }>(paths.tags.one(modal.tag.id), { name: formName.trim(), color: formColor });
        if (data.success) {
          setActionMsg(t("tags.toast.updated"));
          setModal(null);
          loadTags();
        } else {
          setActionMsg(data.error || t("tags.toast.update_failed"));
        }
      }
    } catch {
      setActionMsg(t("tags.toast.failed"));
    }
  };

  const handleDelete = async () => {
    if (!deleteConfirm) return;
    try {
      const data = await api.del<{ success?: boolean; error?: string }>(paths.tags.one(deleteConfirm.id));
      if (data.success) {
        setActionMsg(t("tags.toast.deleted"));
        setDeleteConfirm(null);
        loadTags();
      } else {
        setActionMsg(data.error || t("tags.toast.delete_failed"));
      }
    } catch {
      setActionMsg(t("tags.toast.delete_failed"));
    }
  };

  return (
    <PageContainer
      title={t("tags.title")}
      subtitle={`${tags.length} ${t("tags.total")}`}
      actions={
        canWrite ? (
          <Button
            onClick={openCreate}
            className="inline-flex items-center justify-center gap-x-2 px-4 sm:px-5 text-sm font-medium text-primary-foreground min-w-[2.75rem] min-h-[2.75rem]"
          >
            <Plus className="size-4" />
            <span>{t("tags.create")}</span>
          </Button>
        ) : undefined
      }
    >
      {actionMsg && (
        <div className="mb-3 px-4 py-2 bg-primary/10 dark:bg-primary/20 border border-primary/20 dark:border-primary/40 rounded-lg text-sm text-primary dark:text-primary flex items-center justify-between">
          <span>{actionMsg}</span>
          <Button variant="ghost" size="icon-sm" onClick={() => setActionMsg(null)} className="text-primary hover:text-primary" aria-label={t("common.dismiss")}><X className="size-4" /></Button>
        </div>
      )}

      

      <DataState loading={loading} error={error} onRetry={loadTags} empty={!loading && !error && tags.length === 0} emptyIcon={Tag} emptyTitle={t("tags.empty")} emptyMessage={t("tags.empty_desc")} emptyAction={canWrite ? <Button onClick={openCreate} className="inline-flex items-center gap-2 px-4 py-2.5 min-w-[2.75rem] min-h-[2.75rem]"><Plus className="size-4" /><span>{t("tags.create")}</span></Button> : undefined} loadingSkeleton={
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
          {Array.from({ length: 6 }).map((_, i) => (
            <Card key={"skel-" + i} className="p-(--card-spacing)">
              <div className="flex items-center gap-3 mb-3">
                <Skeleton className="size-8 rounded-full" />
                <Skeleton className="h-5 w-24" />
              </div>
              <Skeleton className="h-4 w-16" />
            </Card>
          ))}
        </div>
      }>
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
          {tags.map((tag) => (
            <Card key={tag.id} interactive className="p-(--card-spacing)">
              <div className="flex items-center justify-between mb-3">
                <div className="flex items-center gap-3">
                  <div
                    className="size-8 rounded-full flex items-center justify-center text-white text-xs font-bold shadow-sm"
                    style={{ backgroundColor: tag.color }}
                  >
                    <Tag className="size-4" />
                  </div>
                  <div>
                    <h3 className="font-semibold text-sm text-foreground">{tag.name}</h3>
                    <span className="text-xs text-muted-foreground">{tag.agent_count} agent{tag.agent_count !== 1 ? "s" : ""}</span>
                  </div>
                </div>
                <div className="flex items-center gap-1">
                  {canWrite && (
                    <>
                      <Tooltip>
                        <TooltipTrigger render={<Button
                            variant="ghost"
                            size="icon"
                            onClick={() => openEdit(tag)}
                            className="size-8 text-muted-foreground hover:text-primary min-w-[2.75rem] min-h-[2.75rem]"
                            aria-label={t("tags.a11y_edit")}
                          />}>
                          <Pencil className="size-4" />
                        </TooltipTrigger>
                        <TooltipContent>{t("common.edit")}</TooltipContent>
                      </Tooltip>
                      <Tooltip>
                        <TooltipTrigger render={<Button
                            variant="ghost"
                            size="icon"
                            onClick={() => setDeleteConfirm(tag)}
                            className="size-8 text-muted-foreground hover:text-destructive-ink min-w-[2.75rem] min-h-[2.75rem]"
                            aria-label={t("tags.a11y_delete")}
                          />}>
                          <Trash2 className="size-4" />
                        </TooltipTrigger>
                        <TooltipContent>{t("common.delete")}</TooltipContent>
                      </Tooltip>
                    </>
                  )}
                </div>
              </div>
              <div className="text-xs text-muted-foreground">
                <span>{t("tags.created")} {tag.created_at ? formatTime(tag.created_at) : "-"}</span>
              </div>
            </Card>
          ))}
        </div>
      </DataState>

      <Dialog open={!!modal} onOpenChange={() => setModal(null)}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{modal?.mode === "create" ? t("tags.dialog_create") : t("tags.dialog_edit")}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4">
            <div>
              <Label className="mb-1.5">{t("tags.field_name")}</Label>
              <Input aria-label={t("tags.a11y_name")} name="input-0"
                value={formName}
                onChange={(e) => setFormName(e.target.value)}
                placeholder={t("tags.name_placeholder")}
                className="text-sm h-9"
                autoFocus
                onKeyDown={(e) => { if (e.key === "Enter") handleSave(); }}
              />
            </div>
            <div>
              <Label className="mb-1.5">{t("tags.field_color")}</Label>
              <div className="flex flex-wrap gap-2">
                {TAG_COLORS.map((color) => (
                  <Button
                    variant="ghost"
                    size="icon"
                    key={color}
                    onClick={() => setFormColor(color)}
                    className={"size-8 rounded-full transition-all min-w-[2.75rem] min-h-[2.75rem] " + (formColor === color ? "ring-2 ring-offset-2 ring-primary dark:ring-offset-background" : "hover:scale-110")}
                    style={{ backgroundColor: color }}
                    aria-label={`Select color ${color}`}
                  />
                ))}
              </div>
              <div className="mt-2 flex items-center gap-2">
                <span className="text-xs text-muted-foreground">{t("tags.color_custom")}</span>
                <Input aria-label={t("groups.a11y_color")} name="input-1"
                  type="color"
                  value={formColor}
                  onChange={(e) => setFormColor(e.target.value)}
                  className="size-8 cursor-pointer p-0.5"
                />
                <span className="text-xs font-mono text-muted-foreground">{formColor}</span>
              </div>
            </div>
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setModal(null)}
            >
              {t("tags.cancel")}
            </Button>
            <Button
              type="button"
              onClick={handleSave}
              disabled={!formName.trim()}
            >
              {modal?.mode === "create" ? t("tags.create_btn") : t("tags.save")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Delete Confirmation */}
      <ConfirmModal
        open={!!deleteConfirm}
        title={t("tags.delete_title")}
        message={t("tags.delete_message", { name: deleteConfirm?.name || "" })}
        confirmText={t("common.delete")}
        danger
        onConfirm={handleDelete}
        onCancel={() => setDeleteConfirm(null)}
      />
    </PageContainer>
  );
}
