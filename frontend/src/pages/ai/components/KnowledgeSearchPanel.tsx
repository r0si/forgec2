import { useState } from "react";
import { Search } from "lucide-react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { errorText } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export interface KnowledgeHit {
  chunk_id: number;
  source_id: number;
  source: string;
  collection_id: number;
  chunk_index: number;
  content: string;
  score: number;
}

interface KnowledgeSearchPanelProps {
  selectedCollectionIds: number[];
}

/**
 * Direct, operator-facing search over the selected knowledge collections.
 *
 * The endpoint (IDF-ranked, blinded-token index) previously had no UI: the
 * console could select collections for the copilot but an operator could not
 * ask the knowledge base a question themselves. Results are ranked by the
 * server, so this component renders them in order and shows the score rather
 * than re-sorting client-side.
 */
export function KnowledgeSearchPanel({ selectedCollectionIds }: KnowledgeSearchPanelProps) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const [hits, setHits] = useState<KnowledgeHit[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const run = async () => {
    const q = query.trim();
    if (!q || selectedCollectionIds.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      const results = await api.postJson<KnowledgeHit[]>(paths.ai.knowledgeSearch, {
        query: q,
        collection_ids: selectedCollectionIds,
        limit: 6,
      });
      setHits(Array.isArray(results) ? results : []);
    } catch (err) {
      setError(errorText(t, err, () => t("ai.knowledge_search_failed")));
      setHits(null);
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="space-y-2">
      <label className="block text-sm font-medium" htmlFor="ai-knowledge-query">
        {t("ai.knowledge_search")}
      </label>
      <div className="flex gap-2">
        <Input
          id="ai-knowledge-query"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter") void run();
          }}
          placeholder={t("ai.knowledge_search_placeholder")}
          className="h-9"
          aria-describedby="ai-knowledge-search-hint"
        />
        <Button
          size="sm"
          onClick={() => void run()}
          disabled={busy || !query.trim() || selectedCollectionIds.length === 0}
        >
          <Search className="size-3.5" />
          {t("ai.knowledge_search_go")}
        </Button>
      </div>
      <p id="ai-knowledge-search-hint" className="text-xs text-muted-foreground">
        {selectedCollectionIds.length === 0 ? t("ai.knowledge_search_pick") : t("ai.knowledge_search_hint")}
      </p>
      {error && <p role="alert" className="text-xs text-destructive-ink">{error}</p>}
      {hits !== null && (
        <div className="space-y-1.5">
          {hits.length === 0 ? (
            <p className="text-xs text-muted-foreground">{t("ai.knowledge_search_empty")}</p>
          ) : (
            hits.map((hit) => (
              <div key={hit.chunk_id} className="rounded-lg border border-border bg-card p-2">
                <div className="mb-1 flex items-center gap-2">
                  <span className="min-w-0 flex-1 truncate text-xs font-medium" title={hit.source}>
                    {hit.source || `#${hit.source_id}`}
                  </span>
                  <Badge variant="outline" className="shrink-0 text-micro-sm">#{hit.chunk_index}</Badge>
                  <span className="shrink-0 font-mono text-micro-sm text-muted-foreground">
                    {hit.score.toFixed(2)}
                  </span>
                </div>
                <p className="line-clamp-3 whitespace-pre-wrap text-xs text-muted-foreground">{hit.content}</p>
              </div>
            ))
          )}
        </div>
      )}
    </section>
  );
}
