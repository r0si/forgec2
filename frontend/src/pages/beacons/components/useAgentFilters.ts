
import { useState, useEffect, useCallback } from "react";
import { useDebounce } from "@/lib/hooks/useDebounce";
import type { Beacon } from "./types";

export type AgentSortKey = "hostname" | "username" | "os" | "ip" | "last_seen" | "status" | "failures";
type SortKey = AgentSortKey;
type LinkedFilter = "" | "direct" | "chained";

export function useAgentFilters(beacons: Beacon[]) {
  const [searchInput, setSearchInput] = useState("");
  const searchQuery = useDebounce(searchInput, 300);
  const [statusFilter, setStatusFilter] = useState("");
  const [osFilter, setOsFilter] = useState("");
  const [page, setPage] = useState(1);
  const [sortKey, setSortKey] = useState<SortKey>("last_seen");
  const [sortDir, setSortDir] = useState<"asc" | "desc">("desc");
  const [linkedFilter, setLinkedFilter] = useState<LinkedFilter>("");
  const [tagFilter, setTagFilter] = useState("");
  const [autoRefresh, setAutoRefresh] = useState(false);
  const [viewMode, setViewMode] = useState<"table" | "grid">("table");
  const [groupByHost, setGroupByHost] = useState(false);
  const [visibleCols, setVisibleCols] = useState<Record<string, boolean>>({
    hostname: true, username: true, os: true, ip: true, last_seen: true,
    window: true, lock: true, tasks: true, status: true, version: true,
    sleep: true, jitter: true, failures: true,
  });

  useEffect(() => { setPage(1); }, [searchInput]);

  useEffect(() => { setPage(1); }, [tagFilter]);

  useEffect(() => { setPage(1); }, [sortKey, sortDir]);

  const toggleSort = useCallback((key: SortKey) => {
    if (sortKey === key) setSortDir((d) => (d === "asc" ? "desc" : "asc"));
    else { setSortKey(key); setSortDir("asc"); }
  }, [sortKey]);

  // The linked (direct/chained) filter is applied server-side alongside
  // search/status/os so pagination stays correct across pages.
  const sortedBeacons = beacons;

  return {
    searchInput, setSearchInput,
    searchQuery,
    statusFilter, setStatusFilter,
    osFilter, setOsFilter,
    page, setPage,
    sortKey, sortDir, toggleSort, setSortKey: setSortKey as (k: SortKey) => void, setSortDir,
    linkedFilter, setLinkedFilter,
    tagFilter, setTagFilter,
    autoRefresh, setAutoRefresh,
    viewMode, setViewMode,
    groupByHost, setGroupByHost,
    visibleCols, setVisibleCols,
    sortedBeacons,
  };
}
