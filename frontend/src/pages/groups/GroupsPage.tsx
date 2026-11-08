import { useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useApiResource } from "@/lib/hooks/useApiResource";
import { POLL } from "@/lib/polling";
import { normalizeListEnvelope } from "@/lib/envelope";
import { useI18n } from "@/lib/i18n";
import { PageContainer } from "@/components/ui/page-container";
import { FieldError } from "@/components/ui/field-error";
import { DataState } from "@/components/ui/data-state";
import { toast } from "sonner";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog";
import { ConfirmModal } from "@/components/ui/confirm-modal";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Layers, Plus } from "lucide-react";

interface AgentGroup {
  id: string;
  name: string;
  description: string;
  color: string;
  parent_id: string;
  agent_count: number;
  child_count: number;
  created_by?: string;
  created_at: string;
  updated_at: string;
}

export default function GroupsPage() {
  const { t } = useI18n();
  const { data, loading, error, refresh: fetchGroups } = useApiResource<AgentGroup[]>({
    fetcher: async () => {
      const data = await api.get(paths.groups.list);
      return normalizeListEnvelope(data, ["groups", "data"]) as AgentGroup[];
    },
    toastThrottleMs: POLL.toastThrottle,
    errorMessage: t("groups.toast.load_failed"),
  });
  const groups = data ?? [];
  const [showModal, setShowModal] = useState(false);
  const [editGroup, setEditGroup] = useState<AgentGroup | null>(null);
  const [deleteId, setDeleteId] = useState<string | null>(null);
  const [formName, setFormName] = useState("");
  const [formDesc, setFormDesc] = useState("");
  const [formColor, setFormColor] = useState("#2ecc71");
  const [formParent, setFormParent] = useState("");
  const [formErrors, setFormErrors] = useState<{ name?: string; desc?: string }>({});

  function openCreate() {
    setEditGroup(null);
    setFormName(""); setFormDesc(""); setFormColor("#2ecc71"); setFormParent("");
    setShowModal(true);
  }

  function openEdit(g: AgentGroup) {
    setEditGroup(g);
    setFormName(g.name); setFormDesc(g.description); setFormColor(g.color); setFormParent(g.parent_id);
    setShowModal(true);
  }

  async function handleSave() {
    const errors: { name?: string; desc?: string } = {};
    if (!formName.trim()) errors.name = t("groups.err_name_required");
    else if (formName.trim().length > 64) errors.name = t("groups.err_name_max");
    if (formDesc.length > 200) errors.desc = t("groups.err_desc_max");
    setFormErrors(errors);
    if (Object.keys(errors).length > 0) return;
    try {
      const body = { name: formName.trim(), description: formDesc, color: formColor, parent_id: formParent };
      const data: { success?: boolean; error?: string } = editGroup
        ? await api.putJson(paths.groups.one(editGroup.id), body)
        : await api.postJson(paths.groups.list, body);
      if (data.success) { setShowModal(false); fetchGroups(); toast.success(editGroup ? t("groups.toast.updated") : t("groups.toast.created")); }
      else { toast.error(data.error || t("groups.toast.save_failed")); }
    } catch { toast.error(t("groups.toast.save_failed")); }
  }

  async function handleDelete() {
    if (!deleteId) return;
    const id = deleteId;
    setDeleteId(null);
    try {
      const data: { success?: boolean; error?: string } = await api.del(paths.groups.one(id));
      if (data.success) { fetchGroups(); toast.success(t("groups.toast.deleted")); }
      else { toast.error(data.error || t("groups.toast.delete_failed")); }
    } catch { toast.error(t("groups.toast.delete_failed")); }
  }

  const rootGroups = groups.filter(g => !g.parent_id);
  const hasChildren = (id: string) => groups.some(g => g.parent_id === id);

  function renderGroup(g: AgentGroup, depth = 0) {
    const children = groups.filter(c => c.parent_id === g.id);
    return (
      <div key={g.id}>
        <div
          className="flex justify-between items-center px-3.5 py-2.5 my-1 rounded-lg bg-card hover:bg-muted transition-colors border-l-[3px]"
          style={{ marginLeft: depth * 24, borderLeftColor: g.color }}
        >
          <div className="flex flex-col gap-0.5">
            <span className="text-sm font-semibold text-foreground">{g.name}</span>
            {g.description && <span className="text-xs text-muted-foreground">{g.description}</span>}
            <span className="text-xs-sm text-muted-foreground">{g.agent_count} {t("groups.agent_count")}, {g.child_count} sub-groups{g.created_by ? ` · ${t("groups.created_by")} ${g.created_by}` : ""}{g.created_at ? ` · ${new Date(g.created_at).toLocaleDateString()}` : ""}</span>
          </div>
          <div className="flex gap-1.5">
                <Button variant="outline" size="sm" onClick={() => openEdit(g)}>{t("groups.edit")}</Button>
             <Button variant="destructive" size="sm" onClick={() => setDeleteId(g.id)}
               disabled={g.agent_count > 0 || hasChildren(g.id)}>{t("groups.delete")}</Button>
          </div>
        </div>
        {children.map(c => renderGroup(c, depth + 1))}
      </div>
    );
  }

  return (
    <PageContainer title={t("groups.title")} subtitle={t("groups.subtitle")} actions={<>
        <Button onClick={openCreate}><Plus className="size-4" /> {t("groups.new")}</Button>
      </>}>
      <DataState
        loading={loading}
        error={error}
        onRetry={() => void fetchGroups()}
        empty={!loading && !error && groups.length === 0}
        emptyIcon={Layers}
        emptyTitle={t("groups.empty")}
        emptyMessage={t("groups.empty_desc")}
        emptyAction={<Button onClick={openCreate}>{t("groups.create")}</Button>}
        loadingSkeleton={
          <Card className="p-(--card-spacing) space-y-2">
            {Array.from({ length: 4 }).map((_, i) => (
              <Skeleton key={i} className="h-12 w-full" />
            ))}
          </Card>
        }
      >
        <Card className="p-(--card-spacing)">
          {rootGroups.map(g => renderGroup(g))}
        </Card>
      </DataState>
      <Dialog open={showModal} onOpenChange={setShowModal}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{editGroup ? t("groups.dialog_edit") : t("groups.dialog_create")}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4">
            <div>
              <Label htmlFor="group-name">{t("groups.field_name")} *</Label>
              <Input id="group-name" aria-label={t("groups.a11y_name")} value={formName} onChange={e => { setFormName(e.target.value); if (formErrors.name) setFormErrors({ ...formErrors, name: undefined }); }} placeholder={t("groups.name_placeholder")} aria-invalid={!!formErrors.name} aria-describedby={formErrors.name ? "group-name-error" : undefined} />
              <FieldError id="group-name-error">{formErrors.name}</FieldError>
            </div>
            <div>
              <Label htmlFor="group-desc">{t("groups.field_desc")}</Label>
              <Input id="group-desc" aria-label={t("groups.a11y_desc")} value={formDesc} onChange={e => { setFormDesc(e.target.value); if (formErrors.desc) setFormErrors({ ...formErrors, desc: undefined }); }} placeholder={t("groups.desc_placeholder")} aria-invalid={!!formErrors.desc} aria-describedby={formErrors.desc ? "group-desc-error" : undefined} />
              <FieldError id="group-desc-error">{formErrors.desc}</FieldError>
            </div>
            <div>
              <Label htmlFor="group-color">{t("groups.field_color")}</Label>
              <Input id="group-color" aria-label={t("groups.a11y_color")} type="color" value={formColor} onChange={e => setFormColor(e.target.value)} />
            </div>
            <div>
              <Label htmlFor="groups-field-parent">{t("groups.field_parent")}</Label>
              <Select id="groups-field-parent" value={formParent || "__none__"} onValueChange={(v) => setFormParent(v === "__none__" || v === null ? "" : v)}>
                <SelectTrigger className="w-full" aria-label={t("groups.a11y_parent")}>
                  <SelectValue placeholder={t("groups.none_root")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__none__">{t("groups.none_root")}</SelectItem>
                  {groups.filter(g => g.id !== editGroup?.id).map(g => (
                    <SelectItem key={g.id} value={g.id}>{g.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowModal(false)}>{t("groups.cancel")}</Button>
            <Button onClick={handleSave}>{editGroup ? t("groups.save") : t("groups.create_btn")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <ConfirmModal open={deleteId !== null} title={t("groups.delete_title")} message={t("groups.delete_msg")} danger onConfirm={handleDelete} onCancel={() => setDeleteId(null)} />
    </PageContainer>
  );
}
