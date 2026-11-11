
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { errorText } from "@/lib/errors";
import { unwrapEnvelope } from "@/lib/envelope";
import { useCachedData } from "@/lib/hooks/useCachedData";
import { useApiResource } from "@/lib/hooks/useApiResource";
import type { Beacon } from "./types";

export type AgentTag = { id: string; name: string; color: string };

interface BeaconQueryParams {
  search: string;
  status: string;
  os: string;
  page: number;
  pageSize: number;
  tag_id: string;
  linked: string;
  sort_key: string;
  sort_dir: string;
}

interface BeaconPage {
  list: Beacon[];
  total: number;
  countMap: Record<string, number>;
}

const DEFAULT_QUERY: BeaconQueryParams = {
  search: "",
  status: "",
  os: "",
  page: 1,
  pageSize: 20,
  tag_id: "",
  linked: "",
  sort_key: "last_seen",
  sort_dir: "desc",
};

export function useAgentData(t: (key: string) => string) {
  const queryRef = useRef<BeaconQueryParams>(DEFAULT_QUERY);
  const [tagsByAgent, setTagsByAgent] = useState<Record<string, AgentTag[]>>({});
  const [tagsError, setTagsError] = useState(false);
  const [agentLocks, setAgentLocks] = useState<Record<string, string>>({});
  // locksLoaded is false until a read has EVER succeeded, so the table can
  // distinguish "this agent is not locked" from "we have never been able to
  // read the lock table". The latter must never render as unlocked.
  const [locksLoaded, setLocksLoaded] = useState(false);
  const [locksError, setLocksError] = useState<string | null>(null);
  const [operatorPresence, setOperatorPresence] = useState<Record<string, string[]>>({});
  const locksAbortRef = useRef<AbortController | null>(null);

  const { data, loading, error, setError, refresh: refreshList, setData } = useApiResource<BeaconPage>({
    fetcher: async (signal) => {
      const q = queryRef.current;
      const p = new URLSearchParams({
        page: String(q.page),
        page_size: String(q.pageSize),
        group: "host",
      });
      if (q.search) p.set("search", q.search);
      if (q.status) p.set("status", q.status);
      if (q.os) p.set("os", q.os);
      if (q.tag_id) p.set("tag_id", q.tag_id);
      if (q.linked) p.set("linked", q.linked);
      p.set("sort_key", q.sort_key);
      p.set("sort_dir", q.sort_dir);
      const d = await api.get<{ agents?: Beacon[]; total?: number | string }>(
        paths.agents.list(p.toString()),
        { signal, unwrap: false },
      );
      // The list arrives inside the standard envelope, so the payload is one
      // level down. Reading it at the top level returned no rows at all.
      const body = unwrapEnvelope(d) as { agents?: Beacon[]; total?: number | string };
      const list = (body.agents || []) as Beacon[];
      const countMap: Record<string, number> = {};
      for (const b of list) {
        const aid = b.id || "";
        if (!aid) continue;
        const ts = b.taskStats;
        countMap[aid] = ts
          ? (ts.pending || 0) + (ts.running || 0) + (ts.completed || 0) + (ts.failed || 0)
          : 0;
      }
      return { list, total: Number(d.total) || list.length, countMap };
    },
    errorMessage: t("agents.load_failed"),
  });

  const beacons = useMemo(() => data?.list ?? [], [data]);
  const total = data?.total ?? 0;
  const taskCountMap = useMemo(() => data?.countMap ?? {}, [data]);

  const loadBeacons = useCallback(
    (search = "", status = "", os = "", page = 1, pageSize = 20, tag_id = "", linked = "", sort_key = "last_seen", sort_dir = "desc") => {
      queryRef.current = { search, status, os, page, pageSize, tag_id, linked, sort_key, sort_dir };
      void refreshList();
    },
    [refreshList],
  );

  const setBeacons = useCallback<React.Dispatch<React.SetStateAction<Beacon[]>>>(
    (action) => {
      setData((prev) => {
        const base = prev?.list ?? [];
        const next = typeof action === "function" ? (action as (b: Beacon[]) => Beacon[])(base) : action;
        return { list: next, total: prev?.total ?? next.length, countMap: prev?.countMap ?? {} };
      });
    },
    [setData],
  );

  const loadLocks = useCallback(() => {
    locksAbortRef.current?.abort();
    const ac = new AbortController();
    locksAbortRef.current = ac;
    setLocksError(null);
    api
      .get<{ agents: Record<string, string>[] }>("/collab/agents", { signal: ac.signal })
      .then((data) => {
        if (ac.signal.aborted) return;
        const agents = data.agents || [];
        const locks: Record<string, string> = {};
        for (const a of agents) {
          if (a.locked_by) locks[a.id] = a.locked_by;
        }
        setAgentLocks(locks);
        setLocksLoaded(true);
      })
      .catch((err: unknown) => {
        if (ac.signal.aborted) return;
        // Keep the last good map so a transient failure does not clear known
        // lock holders; locksLoaded stays as-is to distinguish never-read.
        setLocksError(errorText(t, err, () => t("agents.locks_unreadable")));
      });
  }, [t]);

  useEffect(() => () => locksAbortRef.current?.abort(), []);

  const { data: cachedAllTags, error: tagsListError, refresh: refreshTagsList } = useCachedData<AgentTag[]>("tags:list", {
    fetcher: async () => {
      const d = await api.get(paths.tags.list);
      return (Array.isArray(d) ? d : d.tags || []) as AgentTag[];
    },
    ttlMs: 60_000,
  });

  const allTags = cachedAllTags ?? [];

  const idsKey = useMemo(() => beacons.map((b) => b.id || "").filter(Boolean).sort().join(","), [beacons]);
  const idsKeyRef = useRef(idsKey);
  idsKeyRef.current = idsKey;

  useEffect(() => {
    if (beacons.length === 0) {
      setTagsByAgent({});
      setTagsError(false);
      return;
    }
    const ac = new AbortController();
    const ids = beacons.map((b) => b.id || "").filter(Boolean);
    const keyAtStart = idsKey;
    // Debounce so rapid list refreshes (sorting, pagination, polling) coalesce
    // into a single batch request instead of a burst.
    const timer = setTimeout(() => {
      if (ac.signal.aborted) return;
      api
        .postJson<{ tags: Record<string, AgentTag[]> }>(paths.agents.batchTags, { agent_ids: ids }, { signal: ac.signal })
        // Guard on idsKey as well as abort: a slow earlier response must not
        // overwrite tags fetched for a newer id set.
        .then((d) => {
          if (!ac.signal.aborted && keyAtStart === idsKeyRef.current) {
            setTagsByAgent(d.tags || {});
            setTagsError(false);
          }
        })
        .catch(() => {
          // Retain the previous map: clearing it made every visible agent look
          // untagged, indistinguishable from a successful "no tags" response.
          if (!ac.signal.aborted && keyAtStart === idsKeyRef.current) setTagsError(true);
        });
    }, 200);
    return () => {
      clearTimeout(timer);
      ac.abort();
    };
    // Only refetch when the set of agent ids actually changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [idsKey]);

  return {
    beacons,
    setBeacons,
    loading,
    total,
    error,
    setError,
    allTags,
    tagsListError,
    refreshTagsList,
    tagsByAgent,
    tagsError,
    taskCountMap,
    agentLocks,
    setAgentLocks,
    locksLoaded,
    locksError,
    locksUnread: !locksLoaded,
    operatorPresence,
    setOperatorPresence,
    loadBeacons,
    loadLocks,
  };
}
