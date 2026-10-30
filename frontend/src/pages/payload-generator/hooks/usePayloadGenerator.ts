
import { useEffect, useState, useCallback, useMemo, useRef } from "react";
import { useSearchParams } from "react-router-dom";
import { toast } from "sonner";
import { API_BASE } from "@/lib/constants";
import { paths } from "@/lib/api-paths";
import { useWS } from "@/lib/wsContext";
import { extractProfilePresets, normalizeListeners } from "@/lib/generate-load";
import { downloadFromResponse, downloadBlob } from "@/lib/download";
import { api, getCsrfToken, handleUnauthorized } from "@/lib/api";
import { useI18n } from "@/lib/i18n";
import {
  Listener, ProfilePreset, SharedState, PayloadForms, PayloadStates, PayloadExtras,
  PayloadKey, BinaryForm, UnixForm, AgentModuleInfo, BuildSignature, BuildSignatureStatus,
  createDefaultForms, createDefaultStates,
} from "@/types/generate";
import type { ListenerForm } from "../components/ListenerModal";
import {
  composeC2URL,
  schemeForTransport,
  transportFromListenerScheme,
} from "../components/generate-gate";
import { parseGenerateQuery } from "@/lib/generate-query";

const DEFAULT_UA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36";

const BUILD_SIGNATURE_STATUSES: BuildSignatureStatus[] = ["valid", "untrusted_root", "invalid", "unsigned"];

type BuildOutcome = {
  status: "completed" | "failed" | "timeout" | "aborted";
  error?: string;
  errorHint?: { code: string; text: string };
  signature?: BuildSignature;
};

/** Read the flat signature fields returned by the build status endpoint/WS. */
function readBuildSignature(data: Record<string, unknown>): BuildSignature | undefined {
  const status = data.signature_status;
  if (typeof status !== "string" || !BUILD_SIGNATURE_STATUSES.includes(status as BuildSignatureStatus)) return undefined;
  const str = (key: string) => (typeof data[key] === "string" && data[key] ? data[key] as string : undefined);
  return {
    status: status as BuildSignatureStatus,
    signer: str("signature_signer"),
    issuer: str("signature_issuer"),
    error: str("signature_error"),
  };
}

/** Read the machine-readable failure hint from a build status/WS payload. */
function readBuildErrorHint(data: Record<string, unknown>): { code: string; text: string } | undefined {
  const hint = data.error_hint;
  if (!hint || typeof hint !== "object") return undefined;
  const h = hint as Record<string, unknown>;
  const code = typeof h.code === "string" ? h.code : undefined;
  const text = typeof h.text === "string" ? h.text : undefined;
  if (!code) return undefined;
  return { code, text: text || "" };
}

// The backend budget is 6 minutes for module resolution plus 15 minutes for
// compilation, and requests may wait in the worker queue. Keep the client
// watching at least that long instead of declaring a timeout while the server
// is still building (which previously left a valid artifact orphaned).
const BUILD_WAIT_TIMEOUT_MS = 25 * 60 * 1000;

/*
 * No hard-coded profile catalog on purpose. A frontend copy silently drifts
 * from the server's embedded profiles — the stale "google" preset here said
 * 8s/15% while internal/payload/profiles/google.json says 30s/30% — so a
 * failed or empty profile read used to render the frontend copy with a
 * "Profile locked" badge, as if those numbers were authoritative. The catalog
 * now comes only from GET /api/generate/profiles, and an unreadable catalog
 * leaves the picker empty rather than wrong.
 */

export function usePayloadGenerator() {
  const { t } = useI18n();
  const [searchParams] = useSearchParams();
  const { connected, subscribe } = useWS();
  const connectedRef = useRef(connected);
  useEffect(() => { connectedRef.current = connected; }, [connected]);
  const [listeners, setListeners] = useState<Listener[]>([]);
  const [loading, setLoading] = useState(true);
  const [profilePresets, setProfilePresets] = useState<ProfilePreset[]>([]);
  // A build carries a profile name to the server, which resolves the actual
  // timing/UA. That makes the catalog load a precondition for building, not
  // just a picker population: until it is read, profile-bearing generation
  // must stay blocked rather than proceed against an unknown catalog.
  const [profilesLoaded, setProfilesLoaded] = useState(false);
  const [profilesError, setProfilesError] = useState<string | null>(null);
  // Agent module catalogue (GET /api/generate/modules). Unlike profiles this is
  // advisory: an unread list hides the selector and builds keep including every
  // module (the server's default), so it must not gate generation.
  const [agentModules, setAgentModules] = useState<AgentModuleInfo[]>([]);
  // A failed listener read is a persistent, retriable condition (rendered as a
  // workspace banner), not a transient toast: the last-known registry is kept
  // visible and generation stays gated until a read succeeds.
  const [listenersError, setListenersError] = useState<string | null>(null);
  const [profileLocked, setProfileLocked] = useState(false);
  const savedManualRef = useRef<{ interval: string; jitter: string; ua: string } | null>(null);
  const prevProfileRef = useRef<string>("default");
  const [showListenerModal, setShowListenerModal] = useState(false);
  const [listenerForm, setListenerForm] = useState<ListenerForm>({ name: "", ltype: "http", host: "", port: "8443", proto: "http" });

  const [shared, setShared] = useState<SharedState>({
    listener_id: "", c2_url: "", protocol: "http", beacon_transport: "http",
    interval: "15", jitter: "30",
    ua: DEFAULT_UA, proxy: "", failover: "", crypto_key: "", beacon_key: "", profile: "",
    dns_doh_url: "", dns_dot_addr: "", ssh_user: "forgec2", ssh_password: "", ssh_key: "", ssh_host_key: "",
    mtls_cert: "", mtls_key: "", mtls_ca: "",
  });

  const [forms, setForms] = useState<PayloadForms>(createDefaultForms);
  const [states, setStates] = useState<PayloadStates>(createDefaultStates);
  const [extras, setExtras] = useState<PayloadExtras>({});

  const fileInputRef = useRef<HTMLInputElement>(null);
  const donutFileRef = useRef<HTMLInputElement>(null);
  // One controller per in-flight build waiter. A shared single ref made
  // concurrent builds kill each other ("build_cancelled" for the older one
  // even though it still runs server-side); a set lets each finish while
  // unmount can still cancel everything.
  const asyncAbortsRef = useRef<Set<AbortController>>(new Set());

  // Stop any in-flight async build poll when leaving the page.
  useEffect(() => {
    // The Set instance itself never changes (only its contents), so
    // capturing it here is equivalent to reading the ref at cleanup time.
    const pending = asyncAbortsRef.current;
    return () => {
      for (const ac of pending) ac.abort();
      pending.clear();
    };
  }, []);

  // Load listeners + profiles
  const loadData = useCallback(async () => {
    try {
      const data = await api.get(paths.listeners.list);
      setListeners(normalizeListeners(data));
      setListenersError(null);
    } catch (e) {
      // A failed load keeps the last-known registry (stale but real) instead
      // of wiping it to "empty"; listenersError is what the workspace banner
      // renders and what keeps generation gated until a read succeeds.
      setListenersError(e instanceof Error ? e.message : t("generate.listeners_unreadable"));
    }
    try {
      const profileData = await api.get(paths.generate.profiles);
      // The server lists its embedded profile set, so a catalog that comes
      // back empty did not deliver one. Treat it as unread instead of
      // substituting a catalog we cannot vouch for.
      const next = extractProfilePresets(profileData, []);
      if (next.length === 0) {
        setProfilesError(t("generate.profiles_unreadable"));
      } else {
        setProfilePresets(next);
        setProfilesError(null);
      }
    } catch (e) {
      setProfilesError(e instanceof Error ? e.message : t("generate.profiles_unreadable"));
    }
    try {
      const moduleData = await api.get<{ modules?: unknown }>(paths.generate.modules);
      const list = Array.isArray(moduleData?.modules) ? (moduleData.modules as unknown[]) : [];
      // Defensive shaping: only entries with a usable name/title render.
      setAgentModules(
        list
          .filter((m): m is AgentModuleInfo =>
            !!m && typeof (m as AgentModuleInfo).name === "string" && typeof (m as AgentModuleInfo).title === "string")
          .map((m) => ({ ...m, tasks: Array.isArray(m.tasks) ? m.tasks : [] })),
      );
    } catch {
      // Advisory only: keep the last-known catalogue (if any) and move on.
    } finally {
      // Profiles resolve after listeners; clearing the page-level spinner
      // here (rather than in the listeners block) is what keeps the workspace
      // — and its Generate action — from appearing before the catalog is known.
      setProfilesLoaded(true);
      setLoading(false);
    }
  }, [t]);

  useEffect(() => { loadData(); }, [loadData]);

  // Keep the listener registry current with other operators and after WS
  // reconnects (sync snapshot) — no polling needed.
  useEffect(() => {
    const unsub = subscribe((msg) => {
      if (msg.type === "listener_update" || msg.type === "sync") loadData();
    });
    return unsub;
  }, [subscribe, loadData]);

  // Consume ?listener_id=&os=&arch=&format= (deep link from a listener,
  // build history, or "rebuild from beacon") whenever the search string
  // changes. Re-applying on the same search would clobber a manual listener pick.
  const lastGenerateSearchRef = useRef<string | null>(null);
  useEffect(() => {
    if (loading) return;
    const search = searchParams.toString();
    if (lastGenerateSearchRef.current === search) return;
    lastGenerateSearchRef.current = search;
    try {
      const q = parseGenerateQuery(search);
      if (q.listenerId) {
        const exists = listeners.some((x) => String(x.id) === q.listenerId);
        if (exists) {
          setShared((s) => ({ ...s, listener_id: q.listenerId! }));
        } else {
          toast.warning(t("generate.toast.listener_gone"));
        }
      }
      if (q.arch) {
        const arch = q.arch;
        setForms((f) => ({
          ...f,
          exe: { ...f.exe, arch },
          dll: { ...f.dll, arch },
        }));
      }
    } catch { /* ignore */ }
  }, [loading, listeners, t, searchParams]);

  const getListenerInfo = useCallback((listenerId: string) => {
    const l = listeners.find((x) => String(x.id) === String(listenerId));
    if (!l) return null;
    return {
      scheme: l.scheme || l.type || "http",
      host: l.host || "",
      port: l.port || "",
      type: l.type || "",
      name: l.name || t("generate.unknown_listener"),
    };
  }, [listeners, t]);

  // Shared settings resolved with profile
  const getSharedSettings = useCallback(() => {
    const preset = profilePresets.find((p) => p.name === prevProfileRef.current);
    let interval = shared.interval;
    let jitter = shared.jitter;
    let ua = shared.ua;
    if (prevProfileRef.current !== "default" && prevProfileRef.current !== "" && preset) {
      interval = String(preset.sleep != null && preset.sleep > 0 ? preset.sleep : 15);
      jitter = String(preset.jitter != null && preset.jitter >= 0 ? preset.jitter : 30);
      ua = preset.user_agent || ua;
    }
    return { profile: prevProfileRef.current, interval, jitter, user_agent: ua, proxy: shared.proxy, crypto_key: shared.crypto_key, beacon_key: shared.beacon_key };
  }, [shared, profilePresets]);

  const buildFormData = useCallback((formData: FormData, extra?: Record<string, string>) => {
    const s = getSharedSettings();
    formData.set("profile", s.profile);
    formData.set("interval", s.interval);
    formData.set("jitter", s.jitter);
    formData.set("user_agent", s.user_agent);
    formData.set("proxy", s.proxy);
    formData.set("crypto_key", s.crypto_key);
    formData.set("beacon_key", s.beacon_key);
    formData.set("listener_id", shared.listener_id);
    // mTLS client credentials: only sent when the operator filled them in, so
    // a material-free build keeps its request shape unchanged.
    if (shared.mtls_cert) formData.set("mtls_cert", shared.mtls_cert);
    if (shared.mtls_key) formData.set("mtls_key", shared.mtls_key);
    if (shared.mtls_ca) formData.set("mtls_ca", shared.mtls_ca);
    if (extra) for (const [k, v] of Object.entries(extra)) formData.set(k, v);
  }, [getSharedSettings, shared.listener_id, shared.mtls_cert, shared.mtls_key, shared.mtls_ca]);

  const buildC2URL = useCallback(() => {
    const info = getListenerInfo(shared.listener_id);
    if (!info) return "";
    const scheme = schemeForTransport(info.scheme || info.type || "http", shared.beacon_transport);
    return composeC2URL({
      scheme,
      host: info.host,
      port: info.port,
      failover: shared.failover,
    });
  }, [shared.listener_id, shared.failover, shared.beacon_transport, getListenerInfo]);

  const getProtocol = useCallback(() => {
    if (shared.protocol && shared.protocol !== "http") return shared.protocol;
    const info = getListenerInfo(shared.listener_id);
    if (!info) return "http";
    if (info.scheme === "tcp" || info.scheme === "tls") return "tcp";
    if (info.scheme === "dns" || info.type === "dns") return "dns";
    if (info.scheme === "icmp") return "icmp";
    if (info.scheme === "udp") return "udp";
    if (info.scheme === "quic") return "quic";
    return "http";
  }, [shared.listener_id, shared.protocol, getListenerInfo]);

  const getBeaconTransport = useCallback(() => {
    if (shared.beacon_transport) return shared.beacon_transport;
    const info = getListenerInfo(shared.listener_id);
    if (!info) return "http";
    const scheme = (info.scheme || info.type || "http").toLowerCase();
    if (scheme === "tcp" || scheme === "tls") return "tcp";
    if (scheme === "dns") return "dns";
    if (scheme === "grpc" || scheme === "grpcs") return "grpc";
    if (scheme === "ssh") return "ssh";
    if (scheme === "wss" || scheme === "ws") return "wss";
    if (scheme === "icmp") return "icmp";
    if (scheme === "mtls") return "mtls";
    if (scheme === "h2c") return "h2c";
    if (scheme === "udp") return "udp";
    if (scheme === "quic") return "quic";
    return "http";
  }, [shared.listener_id, shared.beacon_transport, getListenerInfo]);

  // Resolves when the operator WS reports this build as completed/failed.
  // While the socket is unavailable a 2s HTTP fallback poll keeps the build
  // wait from stalling. Cancelled via the abort signal.
  const waitForBuildResult = useCallback((buildId: string, signal: AbortSignal) => {
    return new Promise<BuildOutcome>((resolve) => {
      let settled = false;
      let fallback: ReturnType<typeof setTimeout> | null = null;
      let timeout: ReturnType<typeof setTimeout> | null = null;
      let unsub: () => void = () => {};
      const finish = (outcome: BuildOutcome) => {
        if (settled) return;
        settled = true;
        unsub();
        if (fallback) clearTimeout(fallback);
        if (timeout) clearTimeout(timeout);
        signal.removeEventListener("abort", onAbort);
        resolve(outcome);
      };
      const onAbort = () => finish({ status: "aborted" });
      const poll = async () => {
        if (settled) return;
        if (connectedRef.current) { fallback = setTimeout(poll, 2000); return; }
        try {
          const pollData = await api.get<Record<string, unknown>>(paths.generate.buildStatus(buildId), { signal });
          if (pollData.status === "completed") finish({ status: "completed", signature: readBuildSignature(pollData) });
          else if (pollData.status === "failed") finish({ status: "failed", error: typeof pollData.error === "string" ? pollData.error : undefined, errorHint: readBuildErrorHint(pollData) });
          else fallback = setTimeout(poll, 2000);
        } catch {
          fallback = setTimeout(poll, 2000);
        }
      };
      unsub = subscribe((msg) => {
        if (msg.type !== "build_update" || msg.build_id !== buildId) return;
        if (msg.status === "completed") finish({ status: "completed", signature: readBuildSignature(msg) });
        else if (msg.status === "failed") finish({ status: "failed", error: typeof msg.error === "string" ? msg.error : undefined, errorHint: readBuildErrorHint(msg) });
      });
      timeout = setTimeout(() => finish({ status: "timeout" }), BUILD_WAIT_TIMEOUT_MS);
      signal.addEventListener("abort", onAbort);
      fallback = setTimeout(poll, 100);
    });
  }, [subscribe]);

  // Core generate request — for binary builds (exe/dll/linux/macos) this uses
  // the async build system: POST returns a build_id, then we poll until done
  // and download the result.  Non-binary endpoints (ps1, stager, etc.) still
  // use the synchronous flow where the response body IS the result.
  const handleGenerate = useCallback(async (endpoint: string, formName: string, opts?: { isJson?: boolean; async?: boolean; extra?: Record<string, string> }) => {
    if (!shared.listener_id) {
      toast.error(t("generate.toast.select_listener_first"));
      return null;
    }
    const c2url = buildC2URL();
    const protocol = getProtocol();
    const transport = getBeaconTransport();
    const formData = new FormData();
    formData.set("c2_url", c2url);
    formData.set("protocol", protocol);
    formData.set("beacon_transport", transport);
    if (shared.dns_doh_url) formData.set("dns_doh_url", shared.dns_doh_url);
    if (shared.dns_dot_addr) formData.set("dns_dot_addr", shared.dns_dot_addr);
    if (shared.ssh_user) formData.set("ssh_user", shared.ssh_user);
    if (shared.ssh_password) formData.set("ssh_password", shared.ssh_password);
    if (shared.ssh_key) formData.set("ssh_key", shared.ssh_key);
    if (shared.ssh_host_key) formData.set("ssh_host_key", shared.ssh_host_key);
    buildFormData(formData, opts?.extra);
    try {
      // 1) Start the build (or synchronous request). Same 120s bound as the
      // donut flow: a hung server previously left the UI spinning forever.
      const startController = new AbortController();
      const startTimeout = window.setTimeout(() => startController.abort(), 120000);
      let res: Response;
      try {
        res = await fetch(`${API_BASE}${paths.generate.binary(endpoint)}`, { method: "POST", body: formData, headers: { "X-CSRF-Token": getCsrfToken() }, credentials: "include", signal: startController.signal });
      } finally {
        window.clearTimeout(startTimeout);
      }
      if (!res.ok) {
        handleUnauthorized(res);
        const ct = res.headers.get("content-type") || "";
        if (ct.includes("application/json")) {
          try { const j = await res.json(); return { error: j.error || JSON.stringify(j) }; } catch { /* fallthrough */ }
        }
        const raw = await res.text();
        const msg = raw.length > 200 ? raw.substring(0, 200) + "..." : raw;
        return { error: msg || `Request failed (${res.status})` };
      }

      // Non-async endpoints: return JSON or download file directly
      if (!opts?.async) {
        if (opts?.isJson) return await res.json();
        await downloadFromResponse(res, formName);
        return { success: true };
      }

      // 2) Async build: await completion over the operator WebSocket.
      // The backend pushes build_update events (building/completed/failed);
      // a slow HTTP fallback poll only runs while the socket is down so the
      // build still completes if the WS is unavailable.
      const startData = await res.json();
      const buildId = startData.build_id;
      if (!buildId) return { error: startData.error || t("generate.toast.no_build_id") };

      const ac = new AbortController();
      asyncAbortsRef.current.add(ac);
      try {
        const outcome = await waitForBuildResult(buildId, ac.signal);
        if (outcome.status === "aborted" || ac.signal.aborted) return { error: t("generate.toast.build_cancelled") };
        if (outcome.status === "failed") {
          // Prefer the machine-readable hint text (actionable, stable across compiler versions),
          // then the raw compiler output, then the generic toast.
          const hintMsg = outcome.errorHint?.text;
          return { error: hintMsg || outcome.error || t("generate.toast.build_failed"), hint: outcome.errorHint };
        }
        if (outcome.status === "timeout") return { error: t("generate.toast.build_timed_out") };

        // 3) Download the result
        const { blob, filename } = await api.downloadGet(paths.generate.buildDownload(buildId), formName);
        downloadBlob(blob, filename);
        return { success: true, signature: outcome.signature };
      } finally {
        asyncAbortsRef.current.delete(ac);
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      if (msg.includes("abort")) return { error: t("generate.toast.request_timed_out") };
      return { error: msg };
    }
  }, [shared.listener_id, shared.dns_doh_url, shared.dns_dot_addr, shared.ssh_user, shared.ssh_password, shared.ssh_key, shared.ssh_host_key, buildC2URL, getProtocol, getBeaconTransport, buildFormData, t, waitForBuildResult]);

  // Form updaters
  const updateForm = useCallback(<K extends PayloadKey>(key: K, updater: (prev: PayloadForms[K]) => PayloadForms[K]) => {
    setForms((prev) => ({ ...prev, [key]: updater(prev[key]) }));
  }, []);

  // Binary form helper (exe/dll share same shape)
  const binaryExtra = useCallback((form: BinaryForm): Record<string, string> => ({
    persist: form.persist ? "true" : "",
    skip_tls_verify: form.skip_tls ? "true" : "",
    evasion: form.evasion ? "true" : "",
    ghost_mode: form.ghost_mode ? "true" : "",
    obfuscate: form.obfuscate ? "true" : "",
    filename: form.filename,
    architecture: form.arch,
    domain_front: form.domain_front,
    p2p_mode: form.p2p_mode,
    p2p_parent: form.p2p_parent,
    p2p_listen_addr: form.p2p_listen_addr,
    dns_domain: form.dns_domain,
    dns_server: form.dns_server,
    working_start: form.working_start,
    working_end: form.working_end,
    working_tz: form.working_tz,
    icon_b64: form.icon_b64,
    icon_preset: form.icon_preset,
    disguise_as: form.disguise_as,
    file_description: form.file_description,
    company_name: form.company_name,
    lnk_disguise: form.lnk_disguise ? "true" : "",
    pe_timestamp: form.pe_timestamp,
    pe_sections: form.pe_sections,
    pe_imports: form.pe_imports,
    pe_manifest: form.pe_manifest,
    slim: form.slim ? "true" : "",
    upx: form.upx ? "true" : "",
    win7_compat: form.win7compat ? "true" : "",
    sign_mode: form.sign_mode === "none" ? "" : form.sign_mode,
    sign_org: form.sign_mode === "self" ? form.sign_org : "",
    sign_pfx_b64: form.sign_mode === "pfx" ? form.sign_pfx_b64 : "",
    sign_pfx_password: form.sign_mode === "pfx" ? form.sign_pfx_password : "",
    modules: form.modules.join(","),
  }), []);

  const unixExtra = useCallback((form: UnixForm): Record<string, string> => ({
    persist: form.persist ? "true" : "",
    skip_tls_verify: form.skip_tls ? "true" : "",
    obfuscate: form.obfuscate ? "true" : "",
    slim: form.slim ? "true" : "",
    upx: form.upx ? "true" : "",
    architecture: form.arch || "amd64",
    filename: form.filename,
    domain_front: form.domain_front,
    working_start: form.working_start,
    working_end: form.working_end,
    working_tz: form.working_tz,
    modules: form.modules.join(","),
  }), []);

  // Generic standard handler
  const generateStandard = useCallback(async (
    key: PayloadKey,
    endpoint: string,
    defaultFilename: string,
    extra: Record<string, string>,
    successMsg: string,
    isAsync = false,
  ) => {
    setStates((s) => ({ ...s, [key]: { busy: true, result: "", hint: undefined } }));
    const result = await handleGenerate(endpoint, defaultFilename, { extra, async: isAsync });
    if (result?.error) {
      setStates((s) => ({ ...s, [key]: { busy: false, result: result.error!, hint: result.errorHint } }));
    } else if (result?.success) {
      toast.success(successMsg);
      setStates((s) => ({ ...s, [key]: { busy: false, result: "", signature: result.signature } }));
    } else {
      setStates((s) => ({ ...s, [key]: { busy: false, result: "", hint: undefined } }));
    }
  }, [handleGenerate, ]);

  // Generate handlers
  const handleGenerateBinary = useCallback(async (key: "exe" | "dll") => {
    const form = forms[key];
    const ext = key === "exe" ? "exe" : "dll";
    await generateStandard(key, `/${key}`, `forge_agent.${ext}`, binaryExtra(form), t("generate.toast.key_generated", { key: key.toUpperCase() }), true);
  }, [forms, generateStandard, binaryExtra, t]);

  const handleGeneratePS1 = useCallback(async () => {
    setStates((s) => ({ ...s, ps1: { busy: true, result: "" } }));
    const extra: Record<string, string> = { persist: forms.ps1.persist ? "true" : "", skip_tls_verify: forms.ps1.skip_tls ? "true" : "", filename: forms.ps1.filename };
    const result = await handleGenerate("/ps1", "", { isJson: true, extra });
    if (result?.error) {
      setStates((s) => ({ ...s, ps1: { busy: false, result: result.error! } }));
    } else if (result?.success) {
      toast.success(t("generate.toast.profile_generated", { original_length: result.original_length, obfuscated_len: result.obfuscated_len }));
      setStates((s) => ({ ...s, ps1: { busy: false, result: "" } }));
      setExtras((e) => ({ ...e, ps1: { code: result.code, original_length: result.original_length, obfuscated_len: result.obfuscated_len } }));
    } else {
      setStates((s) => ({ ...s, ps1: { busy: false, result: "" } }));
    }
  }, [forms.ps1, handleGenerate, t]);

  const handleGenerateUnix = useCallback(async (key: "linux" | "macos") => {
    await generateStandard(key, `/${key}`, "forge_implant", unixExtra(forms[key]), t("generate.toast.key_generated", { key: key === "linux" ? "Linux ELF" : "macOS" }), true);
  }, [forms, generateStandard, unixExtra, t]);

  const handleGenerateStager = useCallback(async (key: "stager" | "stager_linux") => {
    const form = forms[key];
    const endpoint = key === "stager" ? "/stager" : "/stager_linux";
    const filename = key === "stager" ? "stager.exe" : "stager";
    const label = key === "stager" ? "Windows" : "Linux";
    await generateStandard(key, endpoint, filename, { filename: form.filename, skip_tls_verify: form.skip_tls ? "true" : "" }, t("generate.toast.loader_generated", { label }));
  }, [forms, generateStandard, t]);

  const handleGenerateShellcode = useCallback(async () => {
    await generateStandard("shellcode", "/shellcode", "shellcode.bin", { command: forms.shellcode.command, filename: forms.shellcode.filename }, t("generate.toast.shellcode_generated"));
  }, [forms.shellcode, generateStandard, t]);

  const handleGenerateDonut = useCallback(async () => {
    setStates((s) => ({ ...s, donut: { busy: true, result: "" } }));
    if (!forms.donut.assembly) {
      toast.error(t("generate.toast.select_assembly"));
      setStates((s) => ({ ...s, donut: { busy: false, result: "" } }));
      return;
    }
    const c2url = buildC2URL();
    const protocol = getProtocol();
    const formData = new FormData();
    formData.set("c2_url", c2url); formData.set("protocol", protocol);
    buildFormData(formData);
    formData.set("arch", forms.donut.arch); formData.set("class", forms.donut.class);
    formData.set("method", forms.donut.method); formData.set("args", forms.donut.args);
    formData.set("filename", forms.donut.filename); formData.set("assembly", forms.donut.assembly);
    try {
      const controller = new AbortController();
      const timeoutId = setTimeout(() => controller.abort(), 120000);
      try {
        const res = await fetch(`${API_BASE}${paths.generate.donut}`, { method: "POST", body: formData, headers: { "X-CSRF-Token": getCsrfToken() }, credentials: "include", signal: controller.signal });
        if (res.ok) {
          await downloadFromResponse(res, "donut_loader.bin");
          toast.success(t("generate.toast.donut_generated"));
          setStates((s) => ({ ...s, donut: { busy: false, result: "" } }));
        } else {
          handleUnauthorized(res);
          const text = await res.text();
          setStates((s) => ({ ...s, donut: { busy: false, result: text } }));
        }
      } finally {
        clearTimeout(timeoutId);
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setStates((s) => ({ ...s, donut: { busy: false, result: msg } }));
    }
  }, [forms.donut, buildC2URL, getProtocol, buildFormData, t]);

  const handleGenerateOneLiner = useCallback(async () => {
    setStates((s) => ({ ...s, oneliner: { busy: true, result: "" } }));
    if (!forms.oneliner.listener_id) {
      toast.error(t("generate.toast.select_listener"));
      setStates((s) => ({ ...s, oneliner: { busy: false, result: "" } }));
      return;
    }
    const info = getListenerInfo(forms.oneliner.listener_id);
    let c2url = "";
    let protocol = "http";
    if (info) {
      c2url = `${info.scheme}://${info.host}:${info.port}`;
      if (info.scheme === "tcp" || info.scheme === "tls") protocol = "tcp";
      else if (info.scheme === "dns" || info.type === "dns") protocol = "dns";
    }
    const formData = new FormData();
    formData.set("c2_url", c2url); formData.set("protocol", protocol);
    formData.set("listener_id", forms.oneliner.listener_id);
    formData.set("payload_type", forms.oneliner.payload_type);
    formData.set("beacon_time", forms.oneliner.beacon_time);
    formData.set("jitter", forms.oneliner.jitter);
    formData.set("skip_tls_verify", forms.oneliner.skip_tls ? "true" : "");
    formData.set("persist", forms.oneliner.persist ? "true" : "");
    const s = getSharedSettings();
    formData.set("profile", s.profile); formData.set("interval", s.interval);
    formData.set("user_agent", s.user_agent); formData.set("proxy", s.proxy);
    formData.set("crypto_key", s.crypto_key);
    formData.set("beacon_key", s.beacon_key);
    try {
      const data = await api.postFormData<{ success?: boolean; error?: string; types?: Array<{ name: string; desc: string; command: string }>; download_url?: string }>(paths.generate.oneLiner, formData);
      if (!data.success) {
        setStates((s) => ({ ...s, oneliner: { busy: false, result: data.error || "" } }));
      } else {
        toast.success(t("generate.toast.oneliner_generated", { count: data.types?.length ?? 0 }));
        setStates((s) => ({ ...s, oneliner: { busy: false, result: "success" } }));
        setExtras((e) => ({ ...e, oneliner: { data: { download_url: data.download_url || "", types: data.types || [] } } }));
      }
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err);
      setStates((s) => ({ ...s, oneliner: { busy: false, result: msg } }));
    }
  }, [forms.oneliner, getListenerInfo, getSharedSettings, t]);

  const sharedRef = useRef(shared);
  useEffect(() => { sharedRef.current = shared; }, [shared]);

  // Profile management — reads latest shared via ref to avoid stale closure
  // (shared changes every keystroke; the callback is passed to memoized
  // children and must not churn or capture outdated interval/jitter/ua).
  const changeProfile = useCallback((profile: string) => {
    if (profile === "__import__") {
      if (fileInputRef.current) fileInputRef.current.click();
      return;
    }
    if (profile === "default" || profile === "") {
      setProfileLocked(false);
      const saved = savedManualRef.current;
      if (saved) {
        setShared((s) => ({ ...s, interval: saved.interval, jitter: saved.jitter, ua: saved.ua, profile }));
      } else {
        setShared((s) => ({ ...s, profile }));
      }
      prevProfileRef.current = profile;
    } else {
      const cur = sharedRef.current;
      if (!profileLocked) savedManualRef.current = { interval: cur.interval, jitter: cur.jitter, ua: cur.ua };
      const preset = profilePresets.find((p) => p.name === profile);
      if (preset) {
        setShared((s) => ({
          ...s,
          interval: String(preset.sleep && preset.sleep > 0 ? preset.sleep : 15),
          jitter: String(preset.jitter && preset.jitter >= 0 ? preset.jitter : 30),
          ua: preset.user_agent || s.ua,
          profile,
        }));
      } else {
        setShared((s) => ({ ...s, profile }));
      }
      setProfileLocked(true);
      prevProfileRef.current = profile;
    }
  }, [profilePresets, profileLocked]);

  const handleProfileImport = useCallback(async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    const fd = new FormData();
    fd.append("profile", file);
    try {
      const data = await api.postFormData<{ success?: boolean; error?: string; profile?: ProfilePreset }>(paths.generate.profileImport, fd);
      if (!data.success) { toast.error(data.error || t("generate.toast.import_failed")); return; }
      const p = data.profile;
      if (!p) return;
      const preset: ProfilePreset = { name: p.name, description: p.description, user_agent: p.user_agent, sleep: p.sleep, jitter: p.jitter, beacon_uri: (p as { beacon_uri?: string }).beacon_uri, method: (p as { method?: string }).method };
      setProfilePresets((prev) => { const idx = prev.findIndex((p) => p.name === preset.name); if (idx >= 0) { const next = [...prev]; next[idx] = preset; return next; } return [...prev, preset]; });
      changeProfile(preset.name);
      toast.success(t("generate.toast.profile_imported"));
    } catch { toast.error(t("common.error")); }
    finally { e.target.value = ""; }
  }, [t, changeProfile]);

  const deleteProfile = useCallback(async (name: string) => {
    if (!name || name === "default") return false;
    try {
      const data = await api.del(paths.generate.profileDelete(name));
      if (data.success) {
        setProfilePresets((prev) => prev.filter((p) => p.name !== name));
        changeProfile("default");
        return true;
      }
    } catch { toast.error(t("generate.toast.delete_profile_failed")); }
    return false;
  }, [changeProfile, t]);

  // Listener management
  const handleCreateListener = useCallback(() => {
    setListenerForm({ name: "My DNS Listener", ltype: "http", host: "c2.example.com", port: "8080", proto: "http" });
    setShowListenerModal(true);
  }, []);

  const submitListener = useCallback(async (form: ListenerForm) => {
    const { name, ltype, host, port, proto } = form;
    try {
      const data = await api.postJson<{ success: boolean; error?: string; listener?: { ID?: number; id?: number } }>(paths.listeners.list, { name, type: ltype, host, port: parseInt(port) || 8080, protocol: proto, enabled: true });
      if (data.success) {
        setShowListenerModal(false);
        toast.success(t("generate.toast.listener_created"));
        loadData();
        const newId = String(data.listener?.ID || data.listener?.id || "");
        if (newId) setShared((prev) => ({ ...prev, listener_id: newId }));
      } else toast.error(t("generate.toast.creation_failed", { error: data.error || "" }));
    } catch { toast.error(t("common.error")); }
  }, [loadData, t]);

  // Keep the C2 preview in sync with listener + selected transport.
  // Transport/protocol snap to the listener default only when listener_id
  // itself changes — editing failover or picking a transport must not
  // overwrite the operator's choice.
  const lastListenerRef = useRef("");
  useEffect(() => {
    if (!shared.listener_id) {
      lastListenerRef.current = "";
      setShared((s) => (s.c2_url === "" ? s : { ...s, c2_url: "" }));
      return;
    }
    const info = getListenerInfo(shared.listener_id);
    if (!info) return;
    const listenerChanged = lastListenerRef.current !== shared.listener_id;
    lastListenerRef.current = shared.listener_id;
    const defaults = transportFromListenerScheme(info.scheme || info.type || "http");
    setShared((s) => {
      const transport = listenerChanged ? defaults.transport : s.beacon_transport;
      const protocol = listenerChanged ? defaults.protocol : s.protocol;
      const scheme = schemeForTransport(info.scheme || info.type || "http", transport);
      const url = composeC2URL({
        scheme,
        host: info.host,
        port: info.port,
        failover: s.failover,
      });
      if (s.c2_url === url && s.beacon_transport === transport && s.protocol === protocol) return s;
      return { ...s, c2_url: url, beacon_transport: transport, protocol };
    });
  }, [shared.listener_id, shared.failover, shared.beacon_transport, getListenerInfo]);

  // Quick presets
  const applyPreset = useCallback((preset: "opsec" | "evasion" | "blend") => {
    const resetBinary = (f: BinaryForm) => ({ ...f, persist: false, skip_tls: false, evasion: false, ghost_mode: false, obfuscate: false, slim: false, upx: false, win7compat: false });
    const maxBinary = (f: BinaryForm) => ({ ...f, persist: true, skip_tls: false, evasion: true, obfuscate: true });
    switch (preset) {
      case "opsec":
        setForms((f) => ({ ...f, exe: resetBinary(f.exe), dll: resetBinary(f.dll) }));
        setShared((s) => ({ ...s, interval: "15", jitter: "30" }));
        break;
      case "evasion":
        setForms((f) => ({ ...f, exe: maxBinary(f.exe), dll: maxBinary(f.dll) }));
        setShared((s) => ({ ...s, interval: "30", jitter: "50" }));
        break;
      case "blend":
        setForms((f) => ({ ...f, exe: resetBinary(f.exe), dll: resetBinary(f.dll) }));
        changeProfile("google");
        break;
    }
  }, [changeProfile]);

  const handlerMap = useMemo(() => ({
    exe: () => handleGenerateBinary("exe"),
    dll: () => handleGenerateBinary("dll"),
    ps1: handleGeneratePS1,
    linux: () => handleGenerateUnix("linux"),
    macos: () => handleGenerateUnix("macos"),
    stager: () => handleGenerateStager("stager"),
    stager_linux: () => handleGenerateStager("stager_linux"),
    shellcode: handleGenerateShellcode,
    donut: handleGenerateDonut,
    oneliner: handleGenerateOneLiner,
  }), [handleGenerateBinary, handleGeneratePS1, handleGenerateUnix, handleGenerateStager, handleGenerateShellcode, handleGenerateDonut, handleGenerateOneLiner]);

  return {
    listeners, listenersError, loading, profilePresets, profileLocked, profilesLoaded, profilesError, loadData,
    agentModules,
    showListenerModal, setShowListenerModal,
    listenerForm, setListenerForm,
    shared, setShared,
    forms, setForms, updateForm,
    states, setStates,
    extras,
    fileInputRef, donutFileRef,
    changeProfile, handleProfileImport, deleteProfile,
    handleCreateListener, submitListener,
    getListenerInfo,
    handlerMap, applyPreset,
  };
}

