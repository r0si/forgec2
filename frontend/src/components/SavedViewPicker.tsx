import { useState } from "react";
import { toastSoon } from "@/lib/toast";
import { useI18n } from "@/lib/i18n";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { useSavedViews } from "@/lib/hooks/useSavedViews";
import { Button } from "@/components/ui/button";
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select";
import { BookmarkPlus, Bookmark, Trash2 } from "lucide-react";

interface SavedViewPickerProps {
  page: string;
  /** Current filter/sort snapshot to persist. */
  getState: () => Record<string, unknown>;
  /** Apply a persisted snapshot back onto the page's state. */
  applyState: (state: Record<string, unknown>) => void;
}

/**
 * SavedViewPicker — named, per-user filter snapshots for a list page.
 * Selecting a view applies its stored filters; the bookmark button saves the
 * current state under a prompted name; the trash button deletes the selected
 * view. Same-name saves overwrite server-side (upsert).
 */
export default function SavedViewPicker({ page, getState, applyState }: SavedViewPickerProps) {
  const { t } = useI18n();
  const { confirm } = useConfirm();
  const { views, loaded, error, save, remove, reload } = useSavedViews(page);
  const [selectedId, setSelectedId] = useState("");
  const [naming, setNaming] = useState(false);
  const [nameInput, setNameInput] = useState("");

  const applySelected = (id: string | null) => {
    setSelectedId(id ?? "");
    if (!id) return;
    const view = views.find((v) => String(v.id) === id);
    if (!view) return;
    try {
      const state = JSON.parse(view.state) as Record<string, unknown>;
      applyState(state);
      toastSoon((toast) => toast.success(t("savedviews.applied", { name: view.name })));
    } catch {
      toastSoon((toast) => toast.error(t("savedviews.corrupt")));
    }
  };

  // A same-name save is an upsert, so writing while the existing views are
  // unknown risks overwriting one of them. Block the write until the list is
  // actually known.
  const viewsKnown = loaded && !error;

  const handleSave = async () => {
    const name = nameInput.trim();
    if (!name || !viewsKnown) return;
    try {
      await save(name, getState());
      // Select the freshly saved view (server upserts same-name entries).
      await new Promise((r) => setTimeout(r, 150));
      setSelectedId("");
      setNaming(false);
      setNameInput("");
      toastSoon((toast) => toast.success(t("savedviews.saved")));
    } catch {
      toastSoon((toast) => toast.error(t("savedviews.save_failed")));
    }
  };

  const handleDelete = async () => {
    if (!selectedId) return;
    const view = views.find((v) => String(v.id) === selectedId);
    if (!view) return;
    if (!(await confirm({ message: t("savedviews.confirm_delete", { name: view.name }) }))) return;
    try {
      await remove(view.id);
      setSelectedId("");
      toastSoon((toast) => toast.success(t("savedviews.deleted")));
    } catch {
      toastSoon((toast) => toast.error(t("savedviews.delete_failed")));
    }
  };

  return (
    <div className="flex items-center gap-1.5">
      <Select value={selectedId} onValueChange={applySelected} disabled={!!error}>
        <SelectTrigger className="w-[170px]" aria-label={t("savedviews.label")}>
          <Bookmark className="size-3.5 mr-1 text-muted-foreground shrink-0" />
          <SelectValue placeholder={error ? t("savedviews.unreadable") : t("savedviews.placeholder")} />
        </SelectTrigger>
        <SelectContent>
          {error ? (
            <div className="px-2 py-1.5 text-xs text-muted-foreground">
              {t("savedviews.unreadable")}
              <button
                type="button"
                onClick={() => void reload()}
                className="ml-1.5 underline underline-offset-2 hover:text-foreground"
              >
                {t("common.try_again")}
              </button>
            </div>
          ) : views.length === 0 ? (
            <div className="px-2 py-1.5 text-xs text-muted-foreground">{t("savedviews.none")}</div>
          ) : (
            views.map((v) => (
              <SelectItem key={v.id} value={String(v.id)}>{v.name}</SelectItem>
            ))
          )}
        </SelectContent>
      </Select>

      {naming ? (
        <>
          <input
            autoFocus
            value={nameInput}
            onChange={(e) => setNameInput(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") void handleSave();
              if (e.key === "Escape") setNaming(false);
            }}
            placeholder={t("savedviews.name_ph")}
            className="h-8 w-32 rounded-lg border border-border bg-transparent px-2 text-xs"
          />
          <Button variant="ghost" size="icon-sm" onClick={() => void handleSave()} disabled={!viewsKnown || !nameInput.trim()} aria-label={t("common.save")} className="text-primary disabled:opacity-50">
            <BookmarkPlus className="size-4" />
          </Button>
          <Button variant="ghost" size="icon-sm" onClick={() => setNaming(false)} aria-label={t("common.cancel")} className="text-muted-foreground">
            <Trash2 className="size-4" />
          </Button>
        </>
      ) : (
        <>
          <Button variant="ghost" size="icon-sm" onClick={() => setNaming(true)}
            disabled={!viewsKnown}
            title={error ? t("savedviews.save_blocked") : t("savedviews.save_current")}
            aria-label={t("savedviews.save_current")}
            className="text-muted-foreground hover:text-primary disabled:opacity-50">
            <BookmarkPlus className="size-4" />
          </Button>
          {selectedId && (
            <Button variant="ghost" size="icon-sm" onClick={() => void handleDelete()}
              title={t("savedviews.delete_current")} aria-label={t("savedviews.delete_current")}
              className="text-muted-foreground hover:text-destructive-ink">
              <Trash2 className="size-4" />
            </Button>
          )}
        </>
      )}
    </div>
  );
}
