import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { errorText } from "@/lib/errors";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";
import type { AgentForm, MalleableForm, PasswordForm, ServerForm, SettingsData } from "./types";

const defaultAgentForm = (): AgentForm => ({
  interval: 5,
  jitter: 10,
  skip_tls: false,
  user_agent: "",
  working_start: "",
  working_end: "",
  working_tz: "",
  beacon_randomization: "linear",
  max_random_interval_ms: 0,
});

const defaultServerForm = (): ServerForm => ({
  log_level: "info",
  tcp_enabled: false,
  tcp_addr: "",
  offline_threshold: 60,
  session_max_age: 24,
  cleanup_retention: 30,
});

const defaultMalleableForm = (): MalleableForm => ({
  enabled: false,
  status_code: 200,
  content_type: "application/json",
  headers_text: "",
  prepend: "",
  append: "",
});

export function useSettingsData() {
  const { t } = useI18n();
  const [data, setData] = useState<SettingsData>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [agentForm, setAgentForm] = useState<AgentForm>(defaultAgentForm);
  const [serverForm, setServerForm] = useState<ServerForm>(defaultServerForm);
  const [malleableForm, setMalleableForm] = useState<MalleableForm>(defaultMalleableForm);
  const [passwordForm, setPasswordForm] = useState<PasswordForm>({ current: "", next: "", confirm: "" });
  const [theme, setTheme] = useState("light");
  const [language, setLanguage] = useState("zh");

  const loadSettings = useCallback(async (signal?: AbortSignal) => {
    setError(null);
    try {
      const d = await api.get<SettingsData>(paths.settings.root, { signal });
      if (signal?.aborted) return;
      setData(d);
      setAgentForm({
        interval: d.default_interval ?? 5,
        jitter: d.default_jitter ?? 10,
        skip_tls: d.default_skip_tls ?? false,
        user_agent: d.default_ua ?? "",
        working_start: d.working_start ?? "",
        working_end: d.working_end ?? "",
        working_tz: d.working_tz ?? "",
        beacon_randomization: d.beacon_randomization || "linear",
        max_random_interval_ms: d.max_random_interval_ms ?? 0,
      });
      setServerForm({
        log_level: d.log_level ?? "info",
        tcp_enabled: d.tcp_enabled ?? false,
        tcp_addr: d.tcp_addr ?? "",
        offline_threshold: d.offline_threshold ?? 60,
        session_max_age: d.session_max_age ?? 24,
        cleanup_retention: d.cleanup_retention ?? 30,
      });
      setMalleableForm({
        enabled: d.malleable_enabled ?? false,
        status_code: d.malleable_status ?? 200,
        content_type: d.malleable_ct ?? "application/json",
        headers_text: d.malleable_headers ?? "",
        prepend: d.malleable_prepend ?? "",
        append: d.malleable_append ?? "",
      });
      setLoaded(true);
      try {
        const storedTheme = localStorage.getItem("forgec2_theme");
        if (storedTheme) setTheme(storedTheme);
        const storedLang = document.cookie.match(/forgec2_lang=([^;]+)/)?.[1]
          || localStorage.getItem("forgec2_lang");
        if (storedLang === "en" || storedLang === "zh") setLanguage(storedLang);
      } catch { /* silent */ }
    } catch (e) {
      if (signal?.aborted || (e instanceof Error && e.name === "AbortError")) return;
      const msg = errorText(t, e, () => t("settings.toast.load_failed"));
      setError(msg);
      toast.error(msg);
    } finally {
      if (!signal?.aborted) setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    const controller = new AbortController();
    void loadSettings(controller.signal);
    return () => controller.abort();
  }, [loadSettings]);

  return {
    data,
    setData,
    loading,
    error,
    loaded,
    loadSettings,
    agentForm,
    setAgentForm,
    serverForm,
    setServerForm,
    malleableForm,
    setMalleableForm,
    passwordForm,
    setPasswordForm,
    theme,
    setTheme,
    language,
    setLanguage,
  };
}
