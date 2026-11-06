
import { useEffect, useState } from "react";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { Spinner } from "@/components/ui/spinner";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { CopyButton } from "@/components/ui/copy-button";
import { Banner } from "@/components/ui/banner";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { CraftPanel } from "@/components/CraftPanel";
import { ConfigSection } from "@/components/ConfigSection";
import { Listener, ProfilePreset, SharedState, clampInterval, clampJitter } from "./types";
import ListenerModal from "./ListenerModal";
import { MtlsCertificateFields } from "./MtlsCertificateFields";
import { FileCode2, Import, KeyRound, Lock, Network, Plus, Radio, Timer, Trash2 } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  isExperimentalTransport,
  visibleBeaconTransports,
  type TransportQuality,
} from "./transport-quality";
import { listenerTransportCompatible } from "./generate-gate";

function TransportQualityLabel({ quality }: { quality: TransportQuality }) {
  const { t } = useI18n();
  if (quality === "core") return <>{t("generate.quality_core")}</>;
  if (quality === "hardened") return <>{t("generate.quality_hardened")}</>;
  return <>{t("generate.quality_experimental")}</>;
}

const CONDITIONAL_TRANSPORTS = ["grpc", "ssh", "wss", "mtls", "h2c", "icmp"];

function SummaryChip({ label, value, tint }: { label: string; value: string; tint: string }) {
  return (
    <span className={cn("inline-flex items-center gap-1 rounded-md px-2 py-1 text-micro-sm font-mono", tint)}>
      <span className="opacity-60">{label}:</span> {value}
    </span>
  );
}

export default function ConnectionPanel({
  listeners,
  shared,
  profilePresets,
  profileLocked,
  showListenerModal,
  listenerForm,
  setShared,
  changeProfile,
  handleCreateListener,
  submitListener,
  setShowListenerModal,
  onProfileDeleted,
  fileInputRef,
  onProfileImport,
}: {
  listeners: Listener[];
  shared: SharedState;
  profilePresets: ProfilePreset[];
  profileLocked: boolean;
  showListenerModal: boolean;
  listenerForm: { name: string; ltype: string; host: string; port: string; proto: string };
  setShared: React.Dispatch<React.SetStateAction<SharedState>>;
  changeProfile: (profile: string) => void;
  handleCreateListener: () => void;
  submitListener: (form: { name: string; ltype: string; host: string; port: string; proto: string }) => void;
  setShowListenerModal: React.Dispatch<React.SetStateAction<boolean>>;
  onProfileDeleted?: (name: string) => boolean | void | Promise<boolean | void>;
  fileInputRef: React.RefObject<HTMLInputElement | null>;
  onProfileImport?: (e: React.ChangeEvent<HTMLInputElement>) => void;
}) {
  const { t } = useI18n();
  const { confirm, modal } = useConfirm();
  const [deleting, setDeleting] = useState(false);
  const [showExperimental, setShowExperimental] = useState(() =>
    isExperimentalTransport(shared.beacon_transport || "http"),
  );
  const currentProfile = profilePresets.find(p => p.name === shared.profile);
  const isBuiltin = !shared.profile || shared.profile === "default" || shared.profile === "";
  const currentListener = listeners.find(l => String(l.id) === String(shared.listener_id));

  const handleDeleteProfile = async () => {
    if (!shared.profile || isBuiltin) return;
    if (!(await confirm({ message: t("generate.confirm_delete_profile", { name: shared.profile }) }))) return;
    setDeleting(true);
    try {
      // deleteProfile swallows API errors and returns success as a boolean —
      // on failure we must NOT flip the selection to default while the
      // profile still exists server-side.
      const ok = await onProfileDeleted?.(shared.profile);
      if (ok === false) return;
      changeProfile("default");
    } catch { toast.error(t("generate.toast.delete_profile_failed")); }
    setDeleting(false);
  };

  const transport = shared.beacon_transport || "http";
  const listenerScheme = currentListener?.scheme || currentListener?.type || "";
  const transportOk = listenerTransportCompatible(listenerScheme, transport, shared.failover);
  const previewUrl = (shared.c2_url || "").split(",")[0] || "";

  useEffect(() => {
    if (isExperimentalTransport(transport)) setShowExperimental(true);
  }, [transport]);

  return (
    <CraftPanel
      title={t("generate.connection_title")}
      badge={<span className="rounded-md bg-primary/10 px-2 py-0.5 text-micro-sm font-mono text-primary uppercase">{transport}</span>}
      bodyClassName="space-y-2.5"
      footer={
        <div className="flex flex-wrap gap-1.5">
          <SummaryChip label={t("generate.connection_listener")} value={currentListener?.name || t("generate.select_listener")} tint="bg-primary/10 text-primary" />
          <SummaryChip label={t("generate.beacon_transport")} value={transport.toUpperCase()} tint="bg-warning/10 text-warning-ink" />
          <SummaryChip label={t("generate.malleable_profile")} value={shared.profile || "default"} tint="bg-muted text-muted-foreground" />
        </div>
      }
    >
      {/* ── Listener & C2 ── */}
      <ConfigSection title={t("generate.connection_listener")} icon={Radio} tone="primary" actions={
        <Button type="button" size="sm" variant="outline" onClick={handleCreateListener} className="gap-1 text-xs">
          <Plus className="size-3.5" /> {t("generate.create_listener")}
        </Button>
      }>
        <div className="space-y-3">
          <div>
            <span className="mb-1.5 block text-xs font-semibold text-muted-foreground">{t("generate.listener_required")}</span>
            <Select value={shared.listener_id} onValueChange={(val) => val != null && setShared(s => ({ ...s, listener_id: val }))}>
              <SelectTrigger className="w-full">
                <SelectValue placeholder={t("generate.select_listener")} />
              </SelectTrigger>
              <SelectContent>
                {listeners.map((l, i) => {
                  const id = String(l.id ?? i);
                  const name = l.name || t("generate.unknown_listener");
                  const scheme = l.scheme || l.type || "http";
                  const host = l.host || "";
                  const port = l.port || "";
                  return <SelectItem key={id} value={id}>{name} ({scheme}://{host}:{port})</SelectItem>;
                })}
              </SelectContent>
            </Select>
          </div>
          <div>
            <span className="mb-1.5 block text-xs font-semibold text-muted-foreground">{t("generate.c2_url_auto")}</span>
            <div className="flex gap-1.5">
              <Input aria-label={t("generate.c2_url_auto")} type="text" readOnly value={shared.c2_url} className="min-w-0 flex-1 font-mono text-xs" />
              <CopyButton text={shared.c2_url} label={t("generate.c2_url_auto")} size="icon-xs" />
            </div>
            {previewUrl ? (
              <p className="mt-1.5 text-micro-sm text-muted-foreground">
                {t("generate.c2_preview", { url: previewUrl, transport: transport.toUpperCase() })}
              </p>
            ) : null}
          </div>
          {!transportOk && shared.listener_id ? (
            <Banner tone="warning" alert>
              {t("generate.transport_mismatch", { transport: transport.toUpperCase(), scheme: listenerScheme || "http" })}
            </Banner>
          ) : null}
          <div>
            <span className="mb-1.5 block text-xs font-semibold text-muted-foreground">{t("generate.failover_c2")}</span>
            <Input aria-label={t("generate.failover_c2")} type="text" placeholder="http://backup1:8080,http://backup2:8080" value={shared.failover} onChange={(e) => setShared(s => ({ ...s, failover: e.target.value }))} className="font-mono text-xs" />
          </div>
        </div>
      </ConfigSection>

      {/* ── Transport ── */}
      <ConfigSection title={t("generate.beacon_transport")} icon={Network} tone="info">
        <div className="space-y-3">
          <div className="grid grid-cols-3 gap-1.5">
            {visibleBeaconTransports(showExperimental).map((tr) => {
              const active = transport === tr.value;
              return (
                <Button
                  key={tr.value}
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    const proto = ["tcp", "dns", "icmp", "udp", "quic"].includes(tr.value) ? tr.value : "http";
                    setShared(s => ({ ...s, beacon_transport: tr.value, protocol: proto }));
                  }}
                  aria-pressed={active}
                  className={cn(
                    "h-auto rounded-lg px-2 py-1.5 text-xs font-medium",
                    active
                      ? "border-primary/40 bg-primary/10 text-primary"
                      : "border-border text-muted-foreground hover:border-primary/30 hover:text-foreground",
                  )}
                >
                  <span className="block">{tr.label}</span>
                  <span className="block text-micro font-normal opacity-70"><TransportQualityLabel quality={tr.quality} /></span>
                </Button>
              );
            })}
          </div>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-auto px-1 text-xs text-muted-foreground"
            onClick={() => setShowExperimental((v) => !v)}
          >
            {showExperimental ? t("generate.hide_experimental") : t("generate.show_experimental")}
          </Button>
          {CONDITIONAL_TRANSPORTS.includes(transport) && (
            <div className="rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-warning-ink">
              <div className="font-semibold mb-0.5">{t("generate.transport_security_title")}</div>
              <div>
                {transport === "grpc" && t("generate.transport_note_grpc")}
                {transport === "ssh" && t("generate.transport_note_ssh")}
                {transport === "wss" && t("generate.transport_note_wss")}
                {transport === "mtls" && t("generate.transport_note_mtls")}
                {transport === "h2c" && t("generate.transport_note_h2c")}
                {transport === "icmp" && t("generate.transport_note_icmp")}
              </div>
            </div>
          )}
          {transport === "dns" && (
            <div className="space-y-2">
              <div>
                <span className="mb-1.5 block text-xs font-semibold text-muted-foreground">{t("generate.doh_url")}</span>
                <Input aria-label={t("generate.doh_url")} type="text" value={shared.dns_doh_url} onChange={(e) => setShared(s => ({ ...s, dns_doh_url: e.target.value }))} placeholder="https://dns.google/dns-query" className="font-mono text-xs" />
              </div>
              <div>
                <span className="mb-1.5 block text-xs font-semibold text-muted-foreground">{t("generate.dot_addr")}</span>
                <Input aria-label={t("generate.dot_addr")} type="text" value={shared.dns_dot_addr} onChange={(e) => setShared(s => ({ ...s, dns_dot_addr: e.target.value }))} placeholder="1.1.1.1:853" className="font-mono text-xs" />
              </div>
            </div>
          )}
          {transport === "ssh" && (
            <div className="space-y-2">
              <div>
                <span className="mb-1.5 block text-xs font-semibold text-muted-foreground">{t("generate.ssh_user")}</span>
                <Input aria-label={t("generate.ssh_user")} type="text" value={shared.ssh_user} onChange={(e) => setShared(s => ({ ...s, ssh_user: e.target.value }))} className="font-mono text-xs" />
              </div>
              <div>
                <span className="mb-1.5 block text-xs font-semibold text-muted-foreground">{t("generate.ssh_password")}</span>
                <Input aria-label={t("generate.ssh_password")} type="password" value={shared.ssh_password} onChange={(e) => setShared(s => ({ ...s, ssh_password: e.target.value }))} className="font-mono text-xs" />
              </div>
              <div>
                <span className="mb-1.5 block text-xs font-semibold text-muted-foreground">{t("generate.ssh_key")}</span>
                <Input aria-label={t("generate.ssh_key")} type="text" value={shared.ssh_key} onChange={(e) => setShared(s => ({ ...s, ssh_key: e.target.value }))} className="font-mono text-xs" />
              </div>
              <div>
                <span className="mb-1.5 block text-xs font-semibold text-muted-foreground">{t("generate.ssh_host_key")}</span>
                <Input
                  aria-label={t("generate.ssh_host_key")}
                  type="text"
                  value={shared.ssh_host_key || ""}
                  onChange={(e) => setShared(s => ({ ...s, ssh_host_key: e.target.value }))}
                  placeholder={t("generate.ssh_host_key_placeholder")}
                  className="font-mono text-xs"
                />
              </div>
            </div>
          )}
          {transport === "mtls" && (
            <MtlsCertificateFields
              value={{ cert: shared.mtls_cert, key: shared.mtls_key, ca: shared.mtls_ca }}
              onChange={(v) => setShared((s) => ({ ...s, mtls_cert: v.cert, mtls_key: v.key, mtls_ca: v.ca }))}
            />
          )}
        </div>
      </ConfigSection>

      {/* ── Profile ── */}
      <ConfigSection title={t("generate.malleable_profile")} icon={FileCode2} tone="violet">
        <div className="space-y-2">
          <div className="flex gap-1.5">
            <Select value={shared.profile || "default"} onValueChange={(val) => val != null && changeProfile(val)}>
              <SelectTrigger className="min-w-0 flex-1">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {profilePresets.map((p) => (
                  <SelectItem key={p.name} value={p.name}>
                    {p.description || p.name}
                    {p.sleep != null || p.jitter != null ? ` · ${p.sleep ?? "?"}s/${p.jitter ?? "?"}%` : ""}
                    {p.beacon_uri ? ` · ${p.beacon_uri}` : ""}
                  </SelectItem>
                ))}
                <SelectItem value="__import__">{t("generate.import_profile")}</SelectItem>
              </SelectContent>
            </Select>
            <Tooltip>
              <TooltipTrigger render={<Button type="button" variant="outline" size="icon" aria-label={t("generate.manage_profiles") || "Manage profiles"} onClick={() => { window.location.href = "/payload-generator?tab=profiles"; }} />}>
                <FileCode2 className="size-4" />
              </TooltipTrigger>
              <TooltipContent>{t("generate.manage_profiles") || "Manage profiles"}</TooltipContent>
            </Tooltip>
            {!isBuiltin && (
              <Tooltip>
                <TooltipTrigger render={<Button type="button" variant="destructive" size="icon" onClick={handleDeleteProfile} disabled={deleting} aria-label={t("generate.delete_profile")} />}>
                  {deleting ? <Spinner size="xs" /> : <Trash2 className="size-4" />}
                </TooltipTrigger>
                <TooltipContent>{t("generate.delete_profile")}</TooltipContent>
              </Tooltip>
            )}
          </div>
          {profileLocked && (
            <p className="flex items-center gap-1 text-xs-sm text-warning-ink">
              <Lock className="size-3.5" />{t("generate.profile_locked")}
            </p>
          )}
          {currentProfile?.description && (
            <p className="text-micro-sm text-muted-foreground">{currentProfile.description}</p>
          )}
        </div>
      </ConfigSection>

      {/* ── Timing ── */}
      <ConfigSection title={t("generate.connection_timing")} icon={Timer} tone="warning">
        <div className="grid grid-cols-2 gap-2">
          <div>
            <span className="mb-1.5 block text-xs font-semibold text-muted-foreground">{t("generate.heartbeat_sec")}</span>
            <Input aria-label={t("generate.heartbeat_sec")} type="number" min={1} max={86400} disabled={profileLocked} value={shared.interval} onChange={(e) => setShared(s => ({ ...s, interval: clampInterval(e.target.value) }))} />
            <p className="mt-1 text-micro-sm text-muted-foreground">{t("generate.heartbeat_range")}</p>
          </div>
          <div>
            <span className="mb-1.5 block text-xs font-semibold text-muted-foreground">{t("generate.jitter_pct")}</span>
            <Input aria-label={t("generate.jitter_pct")} type="number" min={0} max={100} disabled={profileLocked} value={shared.jitter} onChange={(e) => setShared(s => ({ ...s, jitter: clampJitter(e.target.value) }))} />
          </div>
        </div>
        <div className="mt-2 grid grid-cols-2 gap-2">
          <div>
            <span className="mb-1.5 block text-xs font-semibold text-muted-foreground">{t("generate.beacon_randomization")}</span>
            <Select disabled={profileLocked} value={shared.beacon_randomization} onValueChange={(v) => v && setShared(s => ({ ...s, beacon_randomization: v }))}>
              <SelectTrigger aria-label={t("generate.beacon_randomization")} className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="default">{t("generate.rand_default")}</SelectItem>
                <SelectItem value="linear">{t("generate.rand_linear")}</SelectItem>
                <SelectItem value="gaussian">{t("generate.rand_gaussian")}</SelectItem>
                <SelectItem value="exponential">{t("generate.rand_exponential")}</SelectItem>
                <SelectItem value="two_stage">{t("generate.rand_two_stage")}</SelectItem>
                <SelectItem value="adaptive">{t("generate.rand_adaptive")}</SelectItem>
              </SelectContent>
            </Select>
            <p className="mt-1 text-micro-sm text-muted-foreground">{t("generate.beacon_randomization_hint")}</p>
          </div>
          <div>
            <span className="mb-1.5 block text-xs font-semibold text-muted-foreground">{t("generate.max_random_interval_ms")}</span>
            {/* Empty = follow the server default (unset); an explicit 0 means "uncap"
                and is sent as-is, so an operator who set a fleet-wide cap can still
                opt a single build back out of it. */}
            <Input aria-label={t("generate.max_random_interval_ms")} type="number" min={0} max={600000} step={100} placeholder={t("generate.max_random_interval_ms_default")} disabled={profileLocked} value={shared.max_random_interval_ms} onChange={(e) => { const raw = e.target.value; setShared(s => ({ ...s, max_random_interval_ms: raw.trim() === "" ? "" : String(Math.min(600000, Math.max(0, parseInt(raw) || 0))) })); }} />
            <p className="mt-1 text-micro-sm text-muted-foreground">{t("generate.max_random_interval_ms_hint")}</p>
          </div>
        </div>
      </ConfigSection>

      {/* ── Keys & OPSEC ── */}
      <ConfigSection title={t("generate.connection_keys")} icon={KeyRound} tone="success">
        <div className="space-y-3">
          <div>
            <span className="mb-1.5 block text-xs font-semibold text-muted-foreground">{t("generate.user_agent")}</span>
            <Input aria-label={t("generate.user_agent")} type="text" disabled={profileLocked} value={shared.ua} onChange={(e) => setShared(s => ({ ...s, ua: e.target.value }))} className="font-mono text-xs" />
          </div>
          <div>
            <span className="mb-1.5 block text-xs font-semibold text-muted-foreground">{t("generate.http_proxy")}</span>
            <Input aria-label={t("generate.http_proxy")} type="text" placeholder="http://proxy:8080" value={shared.proxy} onChange={(e) => setShared(s => ({ ...s, proxy: e.target.value }))} className="font-mono text-xs" />
          </div>
          <div>
            <span className="mb-1.5 block text-xs font-semibold text-muted-foreground">{t("generate.crypto_key")}</span>
            <Input aria-label={t("generate.crypto_key")} type="text" placeholder="a1b2c3d4..." value={shared.crypto_key} onChange={(e) => setShared(s => ({ ...s, crypto_key: e.target.value }))} className="font-mono text-xs" />
          </div>
          <div>
            <span className="mb-1.5 block text-xs font-semibold text-muted-foreground">{t("generate.beacon_key")}</span>
            <div className="flex gap-1.5">
              <Input aria-label={t("generate.beacon_key")} type="password" placeholder={t("generate.beacon_key_placeholder")} value={shared.beacon_key} onChange={(e) => setShared(s => ({ ...s, beacon_key: e.target.value }))} className="min-w-0 flex-1 font-mono text-xs" />
              <Button type="button" size="sm" variant="outline" className="shrink-0 text-xs" title={t("generate.beacon_key_fill")} onClick={async () => {
                try {
                  const data = await api.get<{ beacon_key: string }>(paths.settings.beaconKey);
                  const k = data?.beacon_key || "";
                  setShared(s => ({ ...s, beacon_key: k }));
                  if (!k) toast.warning(t("generate.beacon_key_empty"));
                  else toast.success(t("generate.beacon_key_filled"));
                } catch {
                  toast.error(t("generate.beacon_key_load_failed"));
                }
              }}>
                <Import className="size-3.5" /> {t("generate.beacon_key_fill")}
              </Button>
            </div>
          </div>
        </div>
      </ConfigSection>

      <Input aria-label={t("generate.import_profile")} name="profile-import" ref={fileInputRef} type="file" accept=".json,application/json" className="hidden" onChange={onProfileImport} />

      <ListenerModal
        show={showListenerModal}
        initial={listenerForm}
        onSubmit={submitListener}
        onClose={() => setShowListenerModal(false)}
      />

      {modal}
    </CraftPanel>
  );
}
