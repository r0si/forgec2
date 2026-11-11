import type { StateCreator } from "zustand";
import { api } from "../api";
import { paths } from "../api-paths";
import { unwrapEnvelope } from "../envelope";
import { logger } from "../logger";
import type { DashboardStats } from "@/types/agent";
import type { AppStore } from "./types";

interface StatsSlice {
  stats: DashboardStats | null;
  statsError?: string;
  fetchStats: () => Promise<void>;
}

let statsInFlight: Promise<void> | null = null;

export const createStatsSlice: StateCreator<AppStore, [], [], StatsSlice> = (set) => ({
  stats: null,

  fetchStats: async () => {
    if (statsInFlight) return statsInFlight;
    statsInFlight = (async () => {
      try {
        // `total_listeners` from the dashboard API counts every row in the
        // listeners table (deployment-global), while the Listeners page shows
        // the tenant-scoped list. Reading the scoped total here keeps the
        // sidebar badge equal to the number the operator can see and manage.
        const [stats, listeners] = await Promise.all([
          api.get<DashboardStats>(paths.dashboard.v1),
          api
            .get<{ total?: number }>(`${paths.listeners.list}?page=1&page_size=1`, { unwrap: false })
            .catch(() => undefined),
        ]);
        // The scoped total lives inside the response envelope; reading it at
        // the top level silently fell back to the deployment-wide count.
        const listenerBody = unwrapEnvelope(listeners) as { total?: number } | undefined;
        const scoped = typeof listenerBody?.total === "number" ? listenerBody.total : undefined;
        set({
          stats: scoped === undefined ? stats : { ...stats, total_listeners: scoped },
          statsError: undefined,
        });
      } catch (e) {
        if (process.env.NODE_ENV === "development") logger.error("fetchStats failed", e);
        set({ statsError: e instanceof Error ? e.message : "" });
      } finally {
        statsInFlight = null;
      }
    })();
    return statsInFlight;
  },
});