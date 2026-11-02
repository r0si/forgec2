import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Progress } from "@/components/ui/progress";
import { useEffect, useState, useCallback } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";
import { Database, Trash2, Loader2 } from "lucide-react";

interface CacheStats {
  enabled: boolean;
  entries: number;
  total_size: number;
  max_size: number;
  max_entries: number;
  hit_count: number;
  miss_count: number;
  hit_rate: number;
}

function formatSize(bytes: number): string {
  if (bytes === 0) return "0 B";
  const k = 1024;
  const sizes = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + " " + sizes[i];
}

export default function BuildCacheCard() {
  const [stats, setStats] = useState<CacheStats | null>(null);
  const [loading, setLoading] = useState(true);
  const [clearing, setClearing] = useState(false);
  const { t } = useI18n();

  const loadStats = useCallback(async () => {
    try {
      const data = await api.get(paths.builds.cacheStats());
      setStats(data as unknown as CacheStats);
    } catch {
      setStats(null);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadStats();
  }, [loadStats]);

  const handleClear = async () => {
    setClearing(true);
    try {
      await api.post(paths.builds.cacheClear());
      toast.success(t("builds.cache_toast_cleared"));
      await loadStats();
    } catch {
      toast.error(t("builds.cache_toast_clear_failed"));
    } finally {
      setClearing(false);
    }
  };

  if (loading) {
    return (
      <Card className="p-(--card-spacing) mb-4">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Database className="size-4 text-muted-foreground" />
            <span className="text-sm font-semibold text-foreground">{t("builds.cache_title")}</span>
          </div>
          <Loader2 className="size-4 animate-spin text-muted-foreground" />
        </div>
      </Card>
    );
  }

  if (!stats || !stats.enabled) {
    return (
      <Card className="p-(--card-spacing) mb-4">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Database className="size-4 text-muted-foreground" />
            <span className="text-sm font-semibold text-foreground">{t("builds.cache_title")}</span>
            <Badge variant="outline" className="px-2 py-0.5 text-micro-sm">
              {t("builds.cache_disabled")}
            </Badge>
          </div>
        </div>
      </Card>
    );
  }

  const hitRatePct = Math.round(stats.hit_rate * 100);
  const sizePct = stats.max_size > 0 ? Math.round((stats.total_size / stats.max_size) * 100) : 0;
  const totalAccesses = stats.hit_count + stats.miss_count;

  return (
    <Card className="p-(--card-spacing) mb-4">
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <Database className="size-4 text-primary" />
          <span className="text-sm font-semibold text-foreground">{t("builds.cache_title")}</span>
          <Badge variant="outline" className="px-2 py-0.5 text-micro-sm">
            {stats.entries} {t("builds.cache_entries")}
          </Badge>
        </div>
        <Button
          variant="outline"
          size="sm"
          onClick={handleClear}
          disabled={clearing || stats.entries === 0}
          className="gap-1.5"
        >
          {clearing ? <Loader2 className="size-3 animate-spin" /> : <Trash2 className="size-3" />}
          {t("builds.cache_clear")}
        </Button>
      </div>

      <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mb-3">
        <div>
          <div className="text-xs text-muted-foreground">{t("builds.cache_hit_rate")}</div>
          <div className="text-lg font-semibold text-foreground">{hitRatePct}%</div>
        </div>
        <div>
          <div className="text-xs text-muted-foreground">{t("builds.cache_hits")}</div>
          <div className="text-lg font-semibold text-foreground">{stats.hit_count}</div>
        </div>
        <div>
          <div className="text-xs text-muted-foreground">{t("builds.cache_misses")}</div>
          <div className="text-lg font-semibold text-foreground">{stats.miss_count}</div>
        </div>
        <div>
          <div className="text-xs text-muted-foreground">{t("builds.cache_size")}</div>
          <div className="text-lg font-semibold text-foreground">{formatSize(stats.total_size)}</div>
        </div>
      </div>

      <div className="space-y-2">
        <div>
          <div className="flex items-center justify-between text-xs text-muted-foreground mb-1">
            <span>{t("builds.cache_hit_rate")}</span>
            <span>{hitRatePct}% ({stats.hit_count}/{totalAccesses})</span>
          </div>
          <Progress value={hitRatePct} className="h-2" />
        </div>
        <div>
          <div className="flex items-center justify-between text-xs text-muted-foreground mb-1">
            <span>{t("builds.cache_size")}</span>
            <span>{formatSize(stats.total_size)} / {formatSize(stats.max_size)}</span>
          </div>
          <Progress value={sizePct} className="h-2" />
        </div>
      </div>
    </Card>
  );
}
