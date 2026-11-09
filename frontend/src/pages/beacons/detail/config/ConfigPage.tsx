import { registerFragment } from "@/lib/i18n";
import { enAgents } from "@/lib/i18n/fragments/en.agents";
registerFragment(enAgents);

import { useEffect, useState, useCallback } from "react";
import { useParams } from "react-router-dom";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";

import { Spinner, PageSpinner } from "@/components/ui/spinner";
import { toast } from "sonner";
import { SectionCard } from "@/components/ui/section-card";
import { PageContainer } from "@/components/ui/page-container";
import { DataError } from "@/components/ui/data-state";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Clock, Info, Plus, RotateCcw, Send, SlidersHorizontal, X } from "lucide-react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useMutation } from "@/lib/hooks/useMutation";
import { useErrorToast } from "@/lib/hooks/useErrorToast";

const COMMON_UAS = [
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36",
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36",
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:120.0) Gecko/20100101 Firefox/120.0",
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36 Edg/138.0.0.0",
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Safari/605.1.15",
  "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36",
];

interface EffectiveConfig {
  sleep: number;
  jitter: number;
  user_agent: string;
  headers: Record<string, string> | null;
  beacon_uri: string;
  method: string;
}

interface ConfigResponse {
  agent_id: string;
  effective: EffectiveConfig;
  has_pending: boolean;
  pending_task_id: number;
}

const defaultConfig: EffectiveConfig = {
  sleep: 10,
  jitter: 20,
  user_agent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
  headers: null,
  beacon_uri: "/beacon",
  method: "POST",
};

export default function AgentConfigPage() {
  const { id } = useParams<{ id: string }>();
  const { t } = useI18n();
  const { toastError } = useErrorToast();
  const [effective, setEffective] = useState<EffectiveConfig | null>(null);
  const [hasPending, setHasPending] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);

  const [editSleep, setEditSleep] = useState("");
  const [editJitter, setEditJitter] = useState("");
  const [editUA, setEditUA] = useState("");
  const [editURI, setEditURI] = useState("");
  const [editMethod, setEditMethod] = useState("POST");
  const [editHeaders, setEditHeaders] = useState<{ key: string; value: string }[]>([]);

  const resetForm = useCallback((cfg: EffectiveConfig) => {
    setEditSleep(String(cfg.sleep));
    setEditJitter(String(cfg.jitter));
    setEditUA(cfg.user_agent);
    setEditURI(cfg.beacon_uri);
    setEditMethod(cfg.method);
    if (cfg.headers) {
      setEditHeaders(Object.entries(cfg.headers).map(([k, v]) => ({ key: k, value: v })));
    } else {
      setEditHeaders([]);
    }
  }, []);

  const loadConfig = useCallback(async () => {
    if (!id) return;
    try {
      const data = await api.get<ConfigResponse>(paths.agents.config(id));
      setEffective(data.effective);
      setHasPending(data.has_pending);
      setLoadError(null);
      resetForm(data.effective);
    } catch (e) {
      // effective stays null. Defaulting it to defaultConfig rendered the
      // placeholder as if it were this agent's real configuration.
      setLoadError(t("agents.config_load_failed"));
      // The inline DataError only renders before the first success; a refresh
      // failure after a push (the form is up) has no other surface.
      toastError(e, "agents.config_load_failed");
    } finally {
      setLoading(false);
    }
  }, [id, resetForm, t, toastError]);

  useEffect(() => { loadConfig(); }, [loadConfig]);

  const addHeader = () => setEditHeaders([...editHeaders, { key: "", value: "" }]);
  const removeHeader = (i: number) => setEditHeaders(editHeaders.filter((_, idx) => idx !== i));
  const updateHeader = (i: number, field: "key" | "value", val: string) => {
    const next = [...editHeaders];
    next[i] = { ...next[i], [field]: val };
    setEditHeaders(next);
  };

  const resetField = (field: string) => {
    switch (field) {
      case "sleep": setEditSleep(String(defaultConfig.sleep)); break;
      case "jitter": setEditJitter(String(defaultConfig.jitter)); break;
      case "ua": setEditUA(defaultConfig.user_agent); break;
      case "uri": setEditURI(defaultConfig.beacon_uri); break;
      case "method": setEditMethod(defaultConfig.method); break;
      case "headers": setEditHeaders([]); break;
    }
  };

  const resetAll = () => resetForm(defaultConfig);

  const { mutate: pushConfig, isPending: pushing, reset: resetPush } = useMutation({
    fn: (agentId: string) => {
      const headersMap: Record<string, string> = {};
      for (const h of editHeaders) {
        if (h.key.trim()) headersMap[h.key.trim()] = h.value;
      }

      const body: Record<string, unknown> = {};
      const s = parseInt(editSleep);
      if (!isNaN(s) && s > 0) body.sleep = s;
      const j = parseInt(editJitter);
      if (!isNaN(j) && j >= 0) body.jitter = j;
      if (editUA.trim()) body.user_agent = editUA.trim();
      if (Object.keys(headersMap).length > 0) body.headers = headersMap;
      if (editURI.trim()) body.beacon_uri = editURI.trim();
      if (editMethod) body.method = editMethod;

      // postJson throws for a `success:false` envelope, so the rejection path
      // below is the only failure surface (the old `res.success` else-branch
      // was dead).
      return api.postJson(paths.agents.config(agentId), body);
    },
    onSuccess: () => {
      toast.success(t("agents.config_push_success"));
      loadConfig();
    },
    onError: (e) => toastError(e, "agents.config_push_failed"),
  });

  const handlePush = () => {
    if (!id) return;
    pushConfig(id);
  };

  // Route reuse: /beacons/A/config → /beacons/B/config keeps this page mounted.
  // The stale form, pending notice and in-flight push must not carry over.
  useEffect(() => {
    setEffective(null);
    setHasPending(false);
    setLoading(true);
    setLoadError(null);
    resetPush();
  }, [id, resetPush]);

  if (loading) {
    return <PageContainer><PageSpinner /></PageContainer>;
  }

  if (!effective) {
    return (
      <PageContainer>
        <DataError message={loadError || t("agents.config_load_failed")} onRetry={() => { setLoading(true); void loadConfig(); }} />
      </PageContainer>
    );
  }

  return (
    <PageContainer>
      {hasPending && (
        <div className="bg-warning/15 border border-warning/30 rounded-lg px-4 py-3 text-sm text-warning-ink flex items-center gap-2">
          <Clock className="size-4" />
          {t("agents.config_pending_push")}
        </div>
      )}

      <SectionCard
        title={t("agents.config_hot_config")}
        icon={SlidersHorizontal}
        tone="primary"
        headerAccent
        className="bg-primary/[0.04]"
      >
        <div className="p-(--card-spacing)">

        <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mb-5">
          <div>
            <Label className="mb-1.5">{t("agents.config_sleep_seconds")}</Label>
            <div className="flex gap-2">
              <Input aria-label={t("agents.config.sleep")} name="input-0" type="number" min="1" value={editSleep} onChange={(e) => setEditSleep(e.target.value)}
                className="flex-1" />
              <Tooltip>
                <TooltipTrigger render={<Button variant="outline" size="sm" onClick={() => resetField("sleep")} aria-label={t("agents.config.reset_sleep")} />}>
                  <RotateCcw className="size-4" />
                </TooltipTrigger>
                <TooltipContent>{t("agents.config_reset_default")}</TooltipContent>
              </Tooltip>
            </div>
          </div>
          <div>
            <Label className="mb-1.5">{t("agents.config_jitter_pct")}</Label>
            <div className="flex gap-2">
              <Input aria-label={t("agents.config.jitter")} name="input-1" type="number" min="0" max="100" value={editJitter} onChange={(e) => setEditJitter(e.target.value)}
                className="flex-1" />
              <Tooltip>
                <TooltipTrigger render={<Button variant="outline" size="sm" onClick={() => resetField("jitter")} aria-label={t("agents.config.reset_jitter")} />}>
                  <RotateCcw className="size-4" />
                </TooltipTrigger>
                <TooltipContent>{t("agents.config_reset_default")}</TooltipContent>
              </Tooltip>
            </div>
          </div>
        </div>

        <div className="mb-4">
          <Label className="mb-1.5">{t("agents.config_user_agent")}</Label>
          <div className="flex gap-2">
            <div className="flex-1 flex gap-2">
              <Input aria-label={t("agents.config.ua")} name="input-2" type="text" value={editUA} onChange={(e) => setEditUA(e.target.value)}
                className="flex-1 font-mono" />
              <Select value="" onValueChange={(v) => { if (v) setEditUA(v); }}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder={t("agents.config_quick_fill")} />
                </SelectTrigger>
                <SelectContent>
                  {COMMON_UAS.map((ua) => (
                    <SelectItem key={ua} value={ua}>{ua.slice(0, 60)}...</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
              <Tooltip>
                <TooltipTrigger render={<Button variant="outline" size="sm" onClick={() => resetField("ua")} aria-label={t("agents.config.reset_ua")} />}>
                  <RotateCcw className="size-4" />
                </TooltipTrigger>
                <TooltipContent>{t("agents.config_reset_default")}</TooltipContent>
              </Tooltip>
          </div>
        </div>

        <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mb-4">
          <div>
            <Label className="mb-1.5">{t("agents.config_beacon_uri")}</Label>
            <div className="flex gap-2">
              <Input aria-label={t("agents.config.uri")} name="input-4" type="text" value={editURI} onChange={(e) => setEditURI(e.target.value)}
                className="flex-1 font-mono" />
              <Tooltip>
                <TooltipTrigger render={<Button variant="outline" size="sm" onClick={() => resetField("uri")} aria-label={t("agents.config.reset_uri")} />}>
                  <RotateCcw className="size-4" />
                </TooltipTrigger>
                <TooltipContent>{t("agents.config_reset_default")}</TooltipContent>
              </Tooltip>
            </div>
          </div>
          <div>
            <Label className="mb-1.5">{t("agents.config_http_method")}</Label>
            <div className="flex gap-2">
              <Select value={editMethod} onValueChange={(v) => { if (v !== null) setEditMethod(v); }}>
                <SelectTrigger className="flex-1">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="GET">GET</SelectItem>
                  <SelectItem value="POST">POST</SelectItem>
                </SelectContent>
              </Select>
              <Tooltip>
                <TooltipTrigger render={<Button variant="outline" size="sm" onClick={() => resetField("method")} aria-label={t("agents.config.reset_method")} />}>
                  <RotateCcw className="size-4" />
                </TooltipTrigger>
                <TooltipContent>{t("agents.config_reset_default")}</TooltipContent>
              </Tooltip>
            </div>
          </div>
        </div>

        <div className="mb-5">
          <div className="flex items-center justify-between mb-1.5">
            <Label>{t("agents.config_custom_headers")}</Label>
            <div className="flex gap-2">
              <Button variant="ghost" size="sm" onClick={addHeader}>
                <Plus className="size-4" /> {t("agents.config_add")}
              </Button>
              <Tooltip>
                <TooltipTrigger render={<Button variant="outline" size="sm" onClick={() => resetField("headers")} aria-label={t("agents.config.reset_headers")} />}>
                  <RotateCcw className="size-4" />
                </TooltipTrigger>
                <TooltipContent>{t("agents.config_reset_default")}</TooltipContent>
              </Tooltip>
            </div>
          </div>
          <div className="space-y-2">
            {editHeaders.length === 0 ? (
              <p className="text-xs text-muted-foreground italic px-1">{t("agents.config_no_headers")}</p>
            ) : (
              editHeaders.map((h, i) => (
                <div key={h.key} className="flex gap-2 items-center">
                  <Input aria-label={t("agents.config.header_name")} name="header-name-6" type="text" placeholder={t("agents.config_header_name")} value={h.key} onChange={(e) => updateHeader(i, "key", e.target.value)}
                    className="flex-1 font-mono" />
                  <Input aria-label={t("agents.config.header_value")} name="value-7" type="text" placeholder={t("agents.config_header_value")} value={h.value} onChange={(e) => updateHeader(i, "value", e.target.value)}
                    className="flex-[2] font-mono" />
                  <Button variant="ghost" size="sm" onClick={() => removeHeader(i)} aria-label={t("agents.config.remove_header")}>
                    <X className="size-4" />
                  </Button>
                </div>
              ))
            )}
          </div>
        </div>

        <div className="flex items-center gap-3 pt-3 border-t border-border">
          <Button onClick={handlePush} disabled={pushing}
            className="gap-2">
            {pushing ? (
              <Spinner size="sm" color="white" />
            ) : (
              <Send className="size-4" />
            )}
            {t("agents.config_push")}
          </Button>
          <Button variant="outline" onClick={resetAll} className="gap-1.5">
            <RotateCcw className="size-4" /> {t("agents.config_reset_all")}
          </Button>
        </div>
        </div>
      </SectionCard>

      <SectionCard
        title={t("agents.config_current_config")}
        icon={Info}
        tone="muted"
        headerAccent
      >
        <div className="p-(--card-spacing)">
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
          <div className="p-3 bg-card border border-border rounded-lg">
            <div className="text-xs text-muted-foreground mb-1">{t("agents.config_sleep_seconds")}</div>
            <div className="text-base font-mono font-semibold text-foreground">{effective.sleep}s</div>
          </div>
          <div className="p-3 bg-card border border-border rounded-lg">
            <div className="text-xs text-muted-foreground mb-1">{t("agents.config_jitter_pct")}</div>
            <div className="text-base font-mono font-semibold text-foreground">{effective.jitter}%</div>
          </div>
          <div className="p-3 bg-card border border-border rounded-lg">
            <div className="text-xs text-muted-foreground mb-1">{t("agents.config_user_agent")}</div>
            <div className="text-xs font-mono text-foreground break-all">{effective.user_agent}</div>
          </div>
          <div className="p-3 bg-card border border-border rounded-lg">
            <div className="text-xs text-muted-foreground mb-1">{t("agents.config_beacon_uri")}</div>
            <div className="text-sm font-mono font-semibold text-foreground">{effective.beacon_uri}</div>
          </div>
          <div className="p-3 bg-card border border-border rounded-lg">
            <div className="text-xs text-muted-foreground mb-1">{t("agents.config_http_method")}</div>
            <div className="text-sm font-mono font-semibold text-foreground">{effective.method}</div>
          </div>
          <div className="p-3 bg-card border border-border rounded-lg">
            <div className="text-xs text-muted-foreground mb-1">{t("agents.config_custom_headers")}</div>
            {effective.headers && Object.keys(effective.headers).length > 0 ? (
              <div className="space-y-0.5">
                {Object.entries(effective.headers).map(([k, v]) => (
                  <div key={k} className="text-xs font-mono text-foreground">
                    <span className="text-primary">{k}</span>: {v}
                  </div>
                ))}
              </div>
            ) : (
              <span className="text-xs text-muted-foreground">{t("agents.config.none")}</span>
            )}
          </div>
        </div>
        </div>
      </SectionCard>
    </PageContainer>
  );
}
