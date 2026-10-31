
import { PageToolbar } from "@/components/ui/page-toolbar";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Checkbox } from "@/components/ui/checkbox";
import { DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem } from "@/components/ui/dropdown-menu";
import { SearchInput } from "@/components/SearchInput";
import { useI18n } from "@/lib/i18n";
import type { Tag } from "./types";
import { Apple, Columns, Monitor, Terminal, Wifi } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import type { ReactNode } from "react";

interface AgentFiltersProps {
  searchInput: string; setSearchInput: (v: string) => void;
  statusFilter: string; setStatusFilter: (v: string) => void;
  osFilter: string; setOsFilter: (v: string) => void;
  tagFilter: string; setTagFilter: (v: string) => void;
  linkedFilter: string; setLinkedFilter: (v: string) => void;
  allTags: Tag[];
  visibleCols: Record<string, boolean>; setVisibleCols: (v: Record<string, boolean> | ((prev: Record<string, boolean>) => Record<string, boolean>)) => void;
  onlineCount: number;
  windowsCount: number; linuxCount: number; darwinCount: number;
  /** Saved-view picker; rendered in the same row as the filters. */
  savedView?: ReactNode;
}

export function AgentFilters({
  searchInput, setSearchInput,
  statusFilter, setStatusFilter,
  osFilter, setOsFilter,
  tagFilter, setTagFilter,
  linkedFilter, setLinkedFilter,
  allTags,
  visibleCols, setVisibleCols,
  onlineCount,
  windowsCount, linuxCount, darwinCount,
  savedView,
}: AgentFiltersProps) {
  const { t } = useI18n();
  return (
    <PageToolbar sticky>
      <div className="flex flex-col gap-3 sm:flex-row">
        <div className="grid grid-cols-2 gap-2 sm:flex sm:flex-1 sm:flex-wrap">
          <SearchInput
            id="agent-search"
            value={searchInput}
            onChange={setSearchInput}
            onClear={() => setSearchInput("")}
            placeholder={t("agents.search_placeholder_short")}
            className="col-span-2 w-full sm:col-span-1 sm:max-w-xs sm:flex-1"
            label={t("common.search")}
          />
          <Select value={statusFilter} onValueChange={(v) => setStatusFilter(v ?? "")}>
            <SelectTrigger aria-label={t("agents.filter_status_aria")} className="w-full sm:w-auto sm:flex-none">
              <SelectValue placeholder={t("agents.all_status")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">{t("agents.all_status")}</SelectItem>
              <SelectItem value="online">{t("agents.online")}</SelectItem>
              <SelectItem value="stale">{t("agents.stale")}</SelectItem>
              <SelectItem value="offline">{t("agents.offline")}</SelectItem>
            </SelectContent>
          </Select>
          <Select value={osFilter} onValueChange={(v) => setOsFilter(v ?? "")}>
            <SelectTrigger aria-label={t("agents.filter_os_aria")} className="w-full sm:w-auto sm:flex-none">
              <SelectValue placeholder={t("agents.all_os")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">{t("agents.all_os")}</SelectItem>
              <SelectItem value="windows">{t("agents.windows")}</SelectItem>
              <SelectItem value="linux">{t("agents.linux")}</SelectItem>
              <SelectItem value="darwin">{t("agents.macos")}</SelectItem>
            </SelectContent>
          </Select>
          <Select value={tagFilter} onValueChange={(v) => setTagFilter(v ?? "")}>
            <SelectTrigger aria-label={t("agents.filter_tag_aria")} className="w-full sm:w-auto sm:flex-none">
              <SelectValue placeholder={t("agents.all_tags")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">{t("agents.all_tags")}</SelectItem>
              {allTags.map((tag) => (
                <SelectItem key={tag.id} value={tag.id}>{tag.name}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={linkedFilter} onValueChange={(v) => setLinkedFilter(v ?? "")}>
            <SelectTrigger aria-label={t("agents.filter_link_aria")} className="w-full sm:w-auto sm:flex-none">
              <SelectValue placeholder={t("agents.all_links")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">{t("agents.all_links")}</SelectItem>
              <SelectItem value="direct">{t("agents.direct_c2")}</SelectItem>
              <SelectItem value="chained">{t("agents.p2p_chained")}</SelectItem>
            </SelectContent>
          </Select>
          <DropdownMenu>
            <DropdownMenuTrigger render={<Button variant="secondary" size="lg" className="hidden w-full gap-2 sm:inline-flex sm:w-auto sm:flex-none" aria-label={t("agents.toggle_columns_aria")} />}>
              <Columns className="size-4" />
              <span className="hidden sm:inline">{t("agents.columns")}</span>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-[160px]">
              {Object.entries(visibleCols).map(([key, vis]) => {
                const labelKey = key === "username" ? "agents.col_user" : `agents.col_${key}`;
                const label = t(labelKey);
                return (
                <DropdownMenuItem key={key} onClick={() => setVisibleCols((p) => ({ ...p, [key]: !vis }))}>
                  <Checkbox
                    checked={vis}
                    onCheckedChange={(checked) => setVisibleCols((p) => ({ ...p, [key]: !!checked }))}
                  />
                  <span>{label !== labelKey ? label : key.replace("_", " ")}</span>
                </DropdownMenuItem>
                );
              })}
            </DropdownMenuContent>
          </DropdownMenu>
          {savedView && (
            <div className="col-span-2 flex items-center sm:col-span-1">{savedView}</div>
          )}
          <div className="hidden sm:flex items-center gap-1.5 ml-auto text-xs-sm">
            {onlineCount > 0 && <Badge variant="secondary" className="gap-1 text-success-ink"><Wifi className="size-4" />{onlineCount}</Badge>}
            {windowsCount > 0 && <Badge variant="secondary" className="gap-1"><Monitor className="size-4" />{windowsCount}</Badge>}
            {linuxCount > 0 && <Badge variant="secondary" className="gap-1"><Terminal className="size-4" />{linuxCount}</Badge>}
            {darwinCount > 0 && <Badge variant="secondary" className="gap-1"><Apple className="size-4" />{darwinCount}</Badge>}
          </div>
        </div>
      </div>
    </PageToolbar>
  );
}
