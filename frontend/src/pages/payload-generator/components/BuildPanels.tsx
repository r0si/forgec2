import React from "react";
import type { ReactNode } from "react";
import { Spinner } from "@/components/ui/spinner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { CopyButton } from "@/components/ui/copy-button";
import type { BinaryForm, UnixForm, PS1Form, StagerForm, ShellcodeForm, DonutForm, BinaryVariant, UnixVariant, StagerVariant, AgentModuleInfo, BuildSignature } from "@/types/generate";
import { AppWindow, Apple, Binary, CheckCircle2, Disc, Download, HardDrive, Info, Package, PackageOpen, Puzzle, Terminal, Wand2 } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { toast } from "sonner";
import { AdvancedSection, FieldLabel, PayloadCard } from "./PayloadCard";
import { BuildResult, BuildStatusBadge } from "./BuildResult";

const BTN_CLASS = "h-11 w-full rounded-xl text-sm font-semibold tracking-tight flex items-center justify-center gap-x-2 accent-gradient text-primary-foreground shadow-md shadow-primary/25 outline-none transition-all duration-200 hover:shadow-lg hover:shadow-primary/35 hover:brightness-110 focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-card active:scale-[0.98] disabled:opacity-50 disabled:pointer-events-none";

// ─── Agent module selector (Go builds) ─────────────────────────

interface ModuleSelectorProps {
  id: string;
  catalog: AgentModuleInfo[];
  /** Included module names; empty = every module (the server's default). */
  value: string[];
  onChange: (next: string[]) => void;
  /** Light profile: the advanced transports are forced out server-side. */
  slimLocked?: boolean;
}

/**
 * Build-time agent module selector. The catalogue comes from the server
 * (GET /api/generate/modules) so the UI cannot drift from what the generator
 * can actually compile. The selection is an explicit include list, but a full
 * selection is normalized back to the empty "all" sentinel: a build made
 * before a module is added to the catalogue should keep including it.
 */
function ModuleSelector({ id, catalog, value, onChange, slimLocked = false }: ModuleSelectorProps) {
  const { t } = useI18n();
  if (catalog.length === 0) return null;
  const allNames = catalog.map((m) => m.name);
  const selected = value.length === 0 ? allNames : value;
  const selectedSet = new Set(selected);
  const toggle = (name: string, checked: boolean) => {
    const next = new Set(selected);
    if (checked) next.add(name);
    else next.delete(name);
    onChange(allNames.every((n) => next.has(n)) ? [] : allNames.filter((n) => next.has(n)));
  };
  const label = (m: AgentModuleInfo) => {
    const key = `generate.modules.${m.name}.title`;
    const translated = t(key);
    return translated === key ? m.title : translated;
  };
  const details = (m: AgentModuleInfo) => {
    const key = `generate.modules.${m.name}.desc`;
    const translated = t(key);
    return translated === key ? m.description : translated;
  };
  const summary = selected.length === allNames.length
    ? t("generate.modules.all")
    : t("generate.modules.selected", { count: selected.length, total: allNames.length });
  return (
    <AdvancedSection title={`${t("generate.modules.title")} — ${summary}`}>
      <p className="mb-2 text-xs text-muted-foreground/80">{t("generate.modules.hint")}</p>
      <div className="grid grid-cols-1 gap-1.5 sm:grid-cols-2" role="group" aria-label={t("generate.modules.aria")}>
        {catalog.map((m) => {
          const locked = slimLocked && m.special === "transports";
          return (
            <label
              key={m.name}
              htmlFor={`${id}-module-${m.name}`}
              className={`flex items-start gap-x-2 rounded-lg border border-border/50 bg-background/40 px-2 py-1.5 transition-colors ${locked ? "opacity-60" : "cursor-pointer hover:border-primary/25 hover:bg-muted/30 has-checked:border-primary/30 has-checked:bg-primary/5"}`}
            >
              <Checkbox
                id={`${id}-module-${m.name}`}
                aria-label={m.name}
                checked={locked ? false : selectedSet.has(m.name)}
                disabled={locked}
                onCheckedChange={(checked) => toggle(m.name, checked === true)}
                className="mt-0.5"
              />
              <span className="text-sm text-muted-foreground">
                {label(m)}
                <span className="block font-normal text-muted-foreground/80 text-xs leading-4">{details(m)}</span>
                {locked && <span className="block font-normal text-warning-ink text-xs leading-4">{t("generate.modules.slim_locked")}</span>}
              </span>
            </label>
          );
        })}
      </div>
    </AdvancedSection>
  );
}

// ─── BinaryPanel (exe / dll) ───────────────────────────────────

interface BinaryPanelProps {
  variant: BinaryVariant;
  form: BinaryForm;
  setForm: React.Dispatch<React.SetStateAction<BinaryForm>>;
  busy: boolean;
  result: ReactNode;
  signature?: BuildSignature;
  hint?: { code: string; text: string };
  onGenerate: () => void;
  canGenerate?: boolean;
  agentModules?: AgentModuleInfo[];
}

const VARIANT_CONFIG: Record<BinaryVariant, { tint: string; icon: ReactNode; titleKey: string; subtitleKey: string; btnKey: string; showP2P: boolean }> = {
  exe: { tint: "bg-warning/10 text-warning-ink", icon: <AppWindow className="size-5" />, titleKey: "generate.panel.exe_title", subtitleKey: "generate.panel.exe_subtitle", btnKey: "generate.panel.generate_exe", showP2P: true },
  dll: { tint: "bg-destructive/10 text-destructive-ink", icon: <Puzzle className="size-5" />, titleKey: "generate.panel.dll_title", subtitleKey: "generate.panel.dll_subtitle", btnKey: "generate.panel.generate_dll", showP2P: false },
};

export const BinaryPanel = React.memo(function BinaryPanel({ variant, form, setForm, busy, result, signature, hint, onGenerate, canGenerate = true, agentModules }: BinaryPanelProps) {
  const { t } = useI18n();
  const cfg = VARIANT_CONFIG[variant];
  const id = `binary-${variant}`;
  return (
    <PayloadCard
      icon={cfg.icon}
      tint={cfg.tint}
      title={t(cfg.titleKey)}
      subtitle={t(cfg.subtitleKey)}
      badge={<BuildStatusBadge busy={busy} result={result} />}
      footer={
        <>
          <Button type="button" onClick={onGenerate} disabled={busy || !canGenerate} title={!canGenerate ? t("generate.toast.select_listener_first") : undefined} className={BTN_CLASS}>
            {busy ? <><Spinner /> {t("generate.panel.generating")}</> : <><Download className="size-4" /> {t(cfg.btnKey)}</>}
          </Button>
          <BuildResult busy={busy} result={result} signature={signature} hint={hint} />
        </>
      }
    >
      <div>
        <FieldLabel>{t("generate.panel.filename")}</FieldLabel>
        <Input aria-label={t("generate.panel.output_filename")} name={`${id}-filename`} value={form.filename} onChange={(e) => setForm({ ...form, filename: e.target.value })} className="bg-background/60 font-mono text-xs transition-colors focus-visible:border-primary/40" placeholder="forge_agent.exe" />
        {(() => {
          const extMap: Record<string,string> = { jpg: ".jpg", pdf: ".pdf", doc: ".docx", xls: ".xlsx", zip: ".zip", txt: ".txt", png: ".png" };
          const ext = extMap[form.disguise_as] || "";
          let preview = form.filename || "forge_agent.exe";
          if (ext && !preview.toLowerCase().includes(ext)) {
            const base = preview.replace(/\.exe$/i, "").replace(/\.jpg$/i, "").replace(/\.pdf$/i, "").replace(/\.docx$/i, "").replace(/\.xlsx$/i, "").replace(/\.zip$/i, "").replace(/\.txt$/i, "").replace(/\.png$/i, "").replace(/\.doc$/i, "").replace(/\.xls$/i, "").replace(/\.jpeg$/i, "");
            preview = base + ext + ".exe";
          }
          if (!preview.toLowerCase().endsWith(".exe") && !preview.toLowerCase().endsWith(".dll")) preview += ".exe";
          const show = preview !== form.filename;
          return show ? <div className="mt-1.5 inline-flex max-w-full items-center gap-1.5 truncate rounded-md bg-primary/10 px-2 py-1 font-mono text-xs text-primary ring-1 ring-primary/20">→ {preview} {form.lnk_disguise ? "+ .lnk" : ""}</div> : null;
        })()}
      </div>
      <div className="space-y-3 rounded-xl border border-border/60 bg-gradient-to-b from-muted/40 to-muted/10 p-3.5 shadow-sm">
        <div className="flex items-center gap-2">
          <div className="size-1.5 rounded-full bg-primary" aria-hidden="true" />
          <FieldLabel className="mb-0">{t("generate.panel.icon")}</FieldLabel>
        </div>
        <div className="grid grid-cols-2 gap-2">
          <div>
            <FieldLabel>{t("generate.panel.icon_preset") || "Icon preset"}</FieldLabel>
            <Select value={form.icon_preset || "__custom"} onValueChange={(val) => val != null && setForm({ ...form, icon_preset: val === "__custom" ? "" : val, icon_b64: val !== "__custom" ? "" : form.icon_b64, icon_file: val !== "__custom" ? null : form.icon_file })}>
              <SelectTrigger className="w-full"><SelectValue placeholder={t("generate.panel.icon_preset_placeholder") || "Choose preset or upload"} /></SelectTrigger>
              <SelectContent>
                <SelectItem value="__custom">{t("generate.panel.icon_custom") || "Custom upload"}</SelectItem>
                <SelectItem value="jpg">JPG Image</SelectItem>
                <SelectItem value="pdf">PDF Document</SelectItem>
                <SelectItem value="word">Word Document</SelectItem>
                <SelectItem value="folder">Folder</SelectItem>
                <SelectItem value="chrome">Chrome</SelectItem>
                <SelectItem value="zip">ZIP Archive</SelectItem>
                <SelectItem value="xls">Excel Sheet</SelectItem>
                <SelectItem value="txt">Text Document</SelectItem>
                <SelectItem value="png">PNG Image</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div>
            <FieldLabel>{t("generate.panel.icon_upload") || "Upload .ico/.png"}</FieldLabel>
            <Input type="file" accept=".ico,.png" onChange={(e) => {
              const file = e.target.files?.[0] || null;
              if (!file) { setForm({ ...form, icon_file: null, icon_b64: "" }); return; }
              if (file.size > 256 * 1024) { toast.error(t("generate.toast.icon_too_large")); return; }
              const reader = new FileReader();
              reader.onload = () => {
                const b64 = (reader.result as string).split(",")[1] || "";
                setForm((prev) => ({ ...prev, icon_file: file, icon_b64: b64, icon_preset: "" }));
              };
              reader.readAsDataURL(file);
            }} />
          </div>
        </div>
        {form.icon_b64 && <div className="flex items-center gap-2 rounded-lg bg-background/60 px-2.5 py-1.5 text-xs text-muted-foreground ring-1 ring-border/40"><img alt={t("generate.panel.icon") || "Icon preview"} src={`data:image/png;base64,${form.icon_b64}`} className="size-6 rounded-md border border-border shadow-sm" /><span className="min-w-0 flex-1 truncate">{t("generate.panel.icon_selected")}: {form.icon_file?.name || "preset"} ({Math.round(form.icon_b64.length * 0.75 / 1024)}KB)</span></div>}
        {form.icon_preset && !form.icon_b64 && (
          <div className="flex items-center gap-2 rounded-lg bg-background/60 px-2.5 py-1.5 text-xs text-muted-foreground ring-1 ring-border/40">
            <span className={`inline-block size-6 shrink-0 rounded-md border border-border shadow-sm ${form.icon_preset === "pdf" ? "bg-destructive" : form.icon_preset === "word" || form.icon_preset === "doc" ? "bg-info" : form.icon_preset === "xls" ? "bg-success" : form.icon_preset === "txt" ? "bg-muted" : form.icon_preset === "png" ? "bg-primary" : form.icon_preset === "zip" ? "bg-warning" : form.icon_preset === "chrome" ? "bg-info" : "bg-primary"}`} aria-hidden="true" />
            <span className="truncate">Preset: {form.icon_preset} · {form.icon_preset === "pdf" ? "PDF" : form.icon_preset === "word" || form.icon_preset === "doc" ? "Word" : form.icon_preset === "xls" ? "Excel" : form.icon_preset}</span>
          </div>
        )}
        <div>
          <FieldLabel>{t("generate.panel.disguise_as") || "Disguise as"}</FieldLabel>
          <Select value={form.disguise_as || ""} onValueChange={(val) => val != null && setForm({ ...form, disguise_as: val === "__none" ? "" : val })}>
            <SelectTrigger className="w-full"><SelectValue placeholder={t("generate.panel.disguise_none")} /></SelectTrigger>
            <SelectContent>
              <SelectItem value="__none">No disguise</SelectItem>
              <SelectItem value="jpg">JPG Image (*.jpg.exe)</SelectItem>
              <SelectItem value="pdf">PDF Document (*.pdf.exe)</SelectItem>
              <SelectItem value="doc">Word Document (*.docx.exe)</SelectItem>
              <SelectItem value="xls">Excel Sheet (*.xlsx.exe)</SelectItem>
              <SelectItem value="zip">ZIP Archive (*.zip.exe)</SelectItem>
              <SelectItem value="txt">Text Document (*.txt.exe)</SelectItem>
              <SelectItem value="png">PNG Image (*.png.exe)</SelectItem>
              <SelectItem value="chrome">Chrome Installer (VersionInfo only)</SelectItem>
              <SelectItem value="folder">Folder (VersionInfo only)</SelectItem>
            </SelectContent>
          </Select>
        </div>
        {form.disguise_as && <div className="rounded-lg border border-warning/25 bg-warning/10 px-2.5 py-1.5 text-xs leading-5 text-warning-ink">{t("generate.panel.disguise_hint")}: *{form.disguise_as === "doc" ? ".docx" : form.disguise_as === "xls" ? ".xlsx" : "."+form.disguise_as}.exe</div>}
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
          <div><FieldLabel>FileDescription</FieldLabel><Input placeholder="JPEG Image" value={form.file_description} onChange={(e) => setForm({ ...form, file_description: e.target.value })} className="bg-background/60 text-xs" /></div>
          <div><FieldLabel>CompanyName</FieldLabel><Input placeholder="Microsoft Corporation" value={form.company_name} onChange={(e) => setForm({ ...form, company_name: e.target.value })} className="bg-background/60 text-xs" /></div>
        </div>
        <label htmlFor={`${id}-lnk`} className="flex cursor-pointer items-center gap-x-2.5 rounded-lg border border-border/50 bg-background/60 px-2.5 py-2 transition-colors hover:border-primary/25 hover:bg-muted/40 has-checked:border-primary/30 has-checked:bg-primary/5">
          <Checkbox id={`${id}-lnk`} checked={form.lnk_disguise} onCheckedChange={(checked) => setForm({ ...form, lnk_disguise: checked === true })} />
          <span className="text-sm text-foreground">Generate .lnk shortcut alongside EXE</span>
        </label>
      </div>
      {cfg.showP2P && (
        <AdvancedSection title={t("generate.panel.p2p_config")}>
          <div>
            <FieldLabel>{t("generate.panel.mode")}</FieldLabel>
            <Select value={form.p2p_mode} onValueChange={(val) => val != null && setForm({ ...form, p2p_mode: val })}>
              <SelectTrigger className="w-full">
                <SelectValue placeholder={t("generate.panel.direct")} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="">{t("generate.panel.direct")}</SelectItem>
                <SelectItem value="parent">{t("generate.panel.p2p_parent")}</SelectItem>
                <SelectItem value="child">{t("generate.panel.p2p_child")}</SelectItem>
                <SelectItem value="dns">{t("generate.panel.dns_tunnel")}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          {form.p2p_mode === "child" && (
            <div>
              <FieldLabel>{t("generate.panel.parent_address")}</FieldLabel>
              <Input aria-label={t("generate.panel.parent_address")} name={`${id}-parent`} type="text" placeholder="tcp://192.168.1.100:4444" value={form.p2p_parent} onChange={(e) => setForm({ ...form, p2p_parent: e.target.value })} className="font-mono text-xs" />
            </div>
          )}
          {form.p2p_mode === "parent" && (
            <div>
              <FieldLabel>{t("generate.panel.listen_address")}</FieldLabel>
              <Input aria-label={t("generate.panel.listen_address")} name={`${id}-listen`} type="text" placeholder="TCP: :4444 / SMB: pipe_name" value={form.p2p_listen_addr} onChange={(e) => setForm({ ...form, p2p_listen_addr: e.target.value })} className="font-mono text-xs" />
            </div>
          )}
          {form.p2p_mode === "dns" && (
            <>
              <div>
                <FieldLabel>{t("generate.panel.dns_domain")}</FieldLabel>
                <Input aria-label={t("generate.panel.dns_domain")} name={`${id}-dns-domain`} type="text" placeholder="c2.example.com" value={form.dns_domain} onChange={(e) => setForm({ ...form, dns_domain: e.target.value })} className="font-mono text-xs" />
              </div>
              <div>
                <FieldLabel>{t("generate.panel.dns_server")}</FieldLabel>
                <Input aria-label={t("generate.panel.dns_server")} name={`${id}-dns-server`} type="text" placeholder="192.168.1.100" value={form.dns_server} onChange={(e) => setForm({ ...form, dns_server: e.target.value })} className="font-mono text-xs" />
              </div>
            </>
          )}
        </AdvancedSection>
      )}
      <div>
        <FieldLabel>{t("generate.panel.architecture")}</FieldLabel>
        <Select value={form.arch} onValueChange={(val) => val != null && setForm({ ...form, arch: val })}>
          <SelectTrigger className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="amd64">x64 (amd64)</SelectItem>
            <SelectItem value="arm64">ARM64</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <AdvancedSection title={t("generate.panel.pe_options") || "PE Tweaks"}>
        <div className="grid grid-cols-2 gap-2">
          <div><FieldLabel>Timestamp</FieldLabel><Select value={form.pe_timestamp || "zero"} onValueChange={(val) => val != null && setForm({ ...form, pe_timestamp: val })}><SelectTrigger className="w-full"><SelectValue>{(v) => (v === "zero" ? "Zero (OPSEC)" : v === "random" ? "Random" : v === "keep" ? "Keep Go" : String(v ?? ""))}</SelectValue></SelectTrigger><SelectContent><SelectItem value="zero">Zero (OPSEC)</SelectItem><SelectItem value="random">Random</SelectItem><SelectItem value="keep">Keep Go</SelectItem></SelectContent></Select></div>
          <div><FieldLabel>Section names</FieldLabel><Select value={form.pe_sections || "default"} onValueChange={(val) => val != null && setForm({ ...form, pe_sections: val })}><SelectTrigger className="w-full"><SelectValue>{(v) => (v === "default" ? "Default" : v === "random" ? "Random" : String(v ?? ""))}</SelectValue></SelectTrigger><SelectContent><SelectItem value="default">Default</SelectItem><SelectItem value="random">Random</SelectItem></SelectContent></Select></div>
          <div><FieldLabel>Benign imports</FieldLabel><Select value={form.pe_imports || "none"} onValueChange={(val) => val != null && setForm({ ...form, pe_imports: val })}><SelectTrigger className="w-full"><SelectValue>{(v) => (v === "none" ? "None" : String(v ?? ""))}</SelectValue></SelectTrigger><SelectContent><SelectItem value="none">None</SelectItem><SelectItem value="kernel32+user32">kernel32+user32</SelectItem></SelectContent></Select></div>
          <div><FieldLabel>Manifest</FieldLabel><Select value={form.pe_manifest || "default"} onValueChange={(val) => val != null && setForm({ ...form, pe_manifest: val })}><SelectTrigger className="w-full"><SelectValue>{(v) => (v === "default" ? "Default Go" : v === "blend" ? "Blend (dpiAware+Win10)" : String(v ?? ""))}</SelectValue></SelectTrigger><SelectContent><SelectItem value="default">Default Go</SelectItem><SelectItem value="blend">Blend (dpiAware+Win10)</SelectItem></SelectContent></Select></div>
        </div>
      </AdvancedSection>
      <AdvancedSection title={t("generate.panel.signing") || "Authenticode Signing"}>
        <div className="space-y-2">
          <div>
            <FieldLabel>{t("generate.panel.sign_mode") || "Signing"}</FieldLabel>
            <Select
              value={form.sign_mode || "none"}
              onValueChange={(val) =>
                val != null &&
                setForm({
                  ...form,
                  sign_mode: val,
                  ...(val !== "pfx"
                    ? { sign_pfx_b64: "", sign_pfx_password: "", sign_pfx_filename: "" }
                    : {}),
                })
              }
            >
              <SelectTrigger className="w-full"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="none">{t("generate.panel.sign_mode_none") || "Unsigned (default)"}</SelectItem>
                <SelectItem value="self">{t("generate.panel.sign_mode_self") || "Self-signed certificate"}</SelectItem>
                <SelectItem value="pfx">{t("generate.panel.sign_mode_pfx") || "PFX certificate"}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          {form.sign_mode === "self" && (
            <div className="space-y-1.5">
              <FieldLabel>{t("generate.panel.sign_org") || "Certificate organization"}</FieldLabel>
              <Input
                value={form.sign_org}
                onChange={(e) => setForm({ ...form, sign_org: e.target.value })}
                placeholder={t("generate.panel.sign_org_placeholder") || "Software Publisher"}
                className="bg-background/60"
              />
              <p className="text-xs leading-4 text-muted-foreground">{t("generate.panel.sign_self_hint")}</p>
            </div>
          )}
          {form.sign_mode === "pfx" && (
            <div className="space-y-2">
              <div>
                <FieldLabel>{t("generate.panel.sign_pfx") || "PFX / P12 file"}</FieldLabel>
                <Input
                  type="file"
                  accept=".pfx,.p12,application/x-pkcs12"
                  aria-label={t("generate.panel.sign_pfx") || "PFX / P12 file"}
                  onChange={(e) => {
                    const file = e.target.files?.[0] || null;
                    if (!file) {
                      setForm({ ...form, sign_pfx_b64: "", sign_pfx_filename: "" });
                      return;
                    }
                    if (file.size > 64 * 1024) {
                      toast.error(t("generate.toast.pfx_too_large"));
                      e.target.value = "";
                      return;
                    }
                    const reader = new FileReader();
                    reader.onload = () => {
                      const b64 = (reader.result as string).split(",")[1] || "";
                      setForm((prev) => ({ ...prev, sign_pfx_b64: b64, sign_pfx_filename: file.name }));
                    };
                    reader.readAsDataURL(file);
                  }}
                />
                {form.sign_pfx_filename && (
                  <p className="mt-1 text-xs text-muted-foreground">
                    {t("generate.panel.sign_pfx_selected", { name: form.sign_pfx_filename })}
                  </p>
                )}
              </div>
              <div>
                <FieldLabel>{t("generate.panel.sign_pfx_password") || "PFX password"}</FieldLabel>
                <Input
                  type="password"
                  autoComplete="new-password"
                  value={form.sign_pfx_password}
                  onChange={(e) => setForm({ ...form, sign_pfx_password: e.target.value })}
                  className="bg-background/60"
                  placeholder="••••••••"
                />
              </div>
              <p className="text-xs leading-4 text-muted-foreground">{t("generate.panel.sign_pfx_hint")}</p>
            </div>
          )}
        </div>
      </AdvancedSection>
      <ModuleSelector id={id} catalog={agentModules ?? []} value={form.modules} onChange={(next) => setForm({ ...form, modules: next })} slimLocked={form.slim} />
      <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        <label htmlFor={`${id}-persist`} className="flex cursor-pointer items-center gap-x-2.5 rounded-lg border border-border/50 bg-background/40 px-2.5 py-2 transition-colors hover:border-primary/25 hover:bg-muted/30 has-checked:border-primary/30 has-checked:bg-primary/5">
          <Checkbox id={`${id}-persist`} aria-label={t("generate.panel.persist_aria")} checked={form.persist} onCheckedChange={(checked) => setForm({ ...form, persist: checked === true })} />
          <span className="text-sm text-foreground">{t("generate.panel.persist")}</span>
        </label>
        <label htmlFor={`${id}-skip-tls`} className="flex cursor-pointer items-center gap-x-2.5 rounded-lg border border-border/50 bg-background/40 px-2.5 py-2 transition-colors hover:border-primary/25 hover:bg-muted/30 has-checked:border-primary/30 has-checked:bg-primary/5">
          <Checkbox id={`${id}-skip-tls`} aria-label={t("generate.panel.skip_tls_aria")} checked={form.skip_tls} onCheckedChange={(checked) => setForm({ ...form, skip_tls: checked === true })} />
          <span className="text-sm text-muted-foreground">{t("generate.panel.skip_tls")}</span>
        </label>
      </div>
      <div className="space-y-2">
        <label htmlFor={`${id}-evasion`} className="flex cursor-pointer items-start gap-x-2.5 rounded-lg border border-border/50 bg-background/40 px-2.5 py-2 transition-colors hover:border-primary/25 hover:bg-muted/30 has-checked:border-primary/30 has-checked:bg-primary/5">
          <Checkbox id={`${id}-evasion`} aria-label={t("generate.panel.edr_evasion_aria")} checked={form.evasion} onCheckedChange={(checked) => setForm({ ...form, evasion: checked === true })} className="mt-0.5" />
          <span className="text-sm text-muted-foreground">
            {t("generate.panel.edr_evasion")}
            <span className="block font-normal text-muted-foreground/80 text-xs leading-4">{t("generate.panel.edr_evasion_hint")}</span>
          </span>
        </label>
        <label htmlFor={`${id}-ghost-mode`} className="flex cursor-pointer items-start gap-x-2.5 rounded-lg border border-border/50 bg-background/40 px-2.5 py-2 transition-colors hover:border-primary/25 hover:bg-muted/30 has-checked:border-primary/30 has-checked:bg-primary/5">
          <Checkbox id={`${id}-ghost-mode`} aria-label={t("generate.panel.ghost_mode_aria")} checked={form.ghost_mode} onCheckedChange={(checked) => setForm({ ...form, ghost_mode: checked === true })} className="mt-0.5" />
          <span className="text-sm text-muted-foreground">
            <span className="inline-flex items-center gap-x-1">
              {t("generate.panel.ghost_mode")}
              <Tooltip>
                <TooltipTrigger type="button" aria-label={t("generate.panel.ghost_mode_tip_aria")} className="rounded-sm text-muted-foreground/70 transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                  <Info className="size-3.5" />
                </TooltipTrigger>
                <TooltipContent side="top">{t("generate.panel.ghost_mode_tip")}</TooltipContent>
              </Tooltip>
            </span>
            <span className="block font-normal text-muted-foreground/80 text-xs leading-4">{t("generate.panel.ghost_mode_hint")}</span>
          </span>
        </label>
      </div>
      {variant === "exe" && (
        <label htmlFor={`${id}-obfuscate`} className="flex cursor-pointer items-start gap-x-2.5 rounded-lg border border-border/50 bg-background/40 px-2.5 py-2 transition-colors hover:border-primary/25 hover:bg-muted/30 has-checked:border-primary/30 has-checked:bg-primary/5">
          <Checkbox id={`${id}-obfuscate`} aria-label={t("generate.panel.obfuscate_aria")} checked={form.obfuscate} onCheckedChange={(checked) => setForm({ ...form, obfuscate: checked === true })} className="mt-0.5" />
          <span className="text-sm text-muted-foreground">
            {t("generate.panel.obfuscate")}
            <span className="block font-normal text-muted-foreground/80 text-xs leading-4">{t("generate.panel.obfuscate_hint")}</span>
          </span>
        </label>
      )}
      <label htmlFor={`${id}-slim`} className="flex cursor-pointer items-start gap-x-2.5 rounded-lg border border-border/50 bg-background/40 px-2.5 py-2 transition-colors hover:border-primary/25 hover:bg-muted/30 has-checked:border-primary/30 has-checked:bg-primary/5">
        <Checkbox id={`${id}-slim`} aria-label={t("generate.panel.slim_aria")} checked={form.slim} onCheckedChange={(checked) => setForm({ ...form, slim: checked === true })} className="mt-0.5" />
        <span className="text-sm text-muted-foreground">
          {t("generate.panel.slim")}
          <span className="block font-normal text-muted-foreground/80 text-xs leading-4">{t("generate.panel.slim_hint")}</span>
        </span>
      </label>
      {variant === "exe" && (
        <label htmlFor={`${id}-upx`} className="flex cursor-pointer items-start gap-x-2.5 rounded-lg border border-border/50 bg-background/40 px-2.5 py-2 transition-colors hover:border-primary/25 hover:bg-muted/30 has-checked:border-primary/30 has-checked:bg-primary/5">
          <Checkbox id={`${id}-upx`} aria-label={t("generate.panel.upx_aria")} checked={form.upx} onCheckedChange={(checked) => setForm({ ...form, upx: checked === true })} className="mt-0.5" />
          <span className="text-sm text-muted-foreground">
            {t("generate.panel.upx")}
            <span className="block font-normal text-muted-foreground/80 text-xs leading-4">{t("generate.panel.upx_hint")}</span>
          </span>
        </label>
      )}
      {variant === "exe" && (
        <label htmlFor={`${id}-win7`} className="flex cursor-pointer items-start gap-x-2.5 rounded-lg border border-border/50 bg-background/40 px-2.5 py-2 transition-colors hover:border-primary/25 hover:bg-muted/30 has-checked:border-primary/30 has-checked:bg-primary/5">
          <Checkbox id={`${id}-win7`} aria-label={t("generate.panel.win7_aria")} checked={form.win7compat} onCheckedChange={(checked) => setForm({ ...form, win7compat: checked === true })} className="mt-0.5" />
          <span className="text-sm text-muted-foreground">
            {t("generate.panel.win7")}
            <span className="block font-normal text-muted-foreground/80 text-xs leading-4">{t("generate.panel.win7_hint")}</span>
          </span>
        </label>
      )}
      <div>
        <FieldLabel>{t("generate.panel.domain_front")}</FieldLabel>
        <Input aria-label={t("generate.panel.domain_front_aria")} name={`${id}-domain-front`} value={form.domain_front} onChange={(e) => setForm({ ...form, domain_front: e.target.value })} placeholder={t("generate.panel.domain_front_aria")} />
      </div>
      <AdvancedSection title={t("generate.panel.working_hours")}>
        <div className="grid grid-cols-3 gap-2">
          <div>
            <FieldLabel>{t("generate.panel.start_hhmm")}</FieldLabel>
            <Input aria-label={t("generate.panel.start_aria")} type="time" value={form.working_start} onChange={(e) => setForm({ ...form, working_start: e.target.value })} />
          </div>
          <div>
            <FieldLabel>{t("generate.panel.end_hhmm")}</FieldLabel>
            <Input aria-label={t("generate.panel.end_aria")} type="time" value={form.working_end} onChange={(e) => setForm({ ...form, working_end: e.target.value })} />
          </div>
          <div>
            <FieldLabel>{t("generate.panel.timezone")}</FieldLabel>
            <Input aria-label={t("generate.panel.timezone_aria")} placeholder="UTC" value={form.working_tz} onChange={(e) => setForm({ ...form, working_tz: e.target.value })} className="font-mono text-xs" />
          </div>
        </div>
      </AdvancedSection>
    </PayloadCard>
  );
});

// ─── UnixPanel (linux / macos) ─────────────────────────────────

interface UnixPanelProps {
  variant: UnixVariant;
  form: UnixForm;
  setForm: React.Dispatch<React.SetStateAction<UnixForm>>;
  busy: boolean;
  result: ReactNode;
  hint?: { code: string; text: string };
  onGenerate: () => void;
  canGenerate?: boolean;
  agentModules?: AgentModuleInfo[];
}

const UNIX_CONFIG: Record<UnixVariant, { tint: string; icon: ReactNode; titleKey: string; subtitleKey: string; btnKey: string }> = {
  linux: { tint: "bg-success/10 text-success-ink", icon: <HardDrive className="size-5" />, titleKey: "generate.panel.elf_title", subtitleKey: "generate.panel.unix_subtitle", btnKey: "generate.panel.generate_elf" },
  macos: { tint: "bg-primary/10 text-primary", icon: <Apple className="size-5" />, titleKey: "generate.panel.macos_title", subtitleKey: "generate.panel.unix_subtitle", btnKey: "generate.panel.generate_macos" },
};

export const UnixPanel = React.memo(function UnixPanel({ variant, form, setForm, busy, result, hint, onGenerate, canGenerate = true, agentModules }: UnixPanelProps) {
  const { t } = useI18n();
  const cfg = UNIX_CONFIG[variant];
  const id = `unix-${variant}`;
  return (
    <PayloadCard
      icon={cfg.icon}
      tint={cfg.tint}
      title={t(cfg.titleKey)}
      subtitle={t(cfg.subtitleKey)}
      badge={<BuildStatusBadge busy={busy} result={result} />}
      footer={
        <>
          <Button type="button" onClick={onGenerate} disabled={busy || !canGenerate} title={!canGenerate ? t("generate.toast.select_listener_first") : undefined} className={BTN_CLASS}>
            {busy ? <><Spinner /> {t("generate.panel.generating")}</> : <><Download className="size-4" /> {t(cfg.btnKey)}</>}
          </Button>
          <BuildResult busy={busy} result={result} hint={hint} />
        </>
      }
    >
      <div>
        <FieldLabel>{t("generate.panel.filename")}</FieldLabel>
        <Input aria-label={t("generate.panel.output_filename")} name={`${id}-filename`} value={form.filename} onChange={(e) => setForm({ ...form, filename: e.target.value })} />
      </div>
      <div>
        <FieldLabel>{t("generate.panel.architecture")}</FieldLabel>
        <Select value={form.arch} onValueChange={(val) => val != null && setForm({ ...form, arch: val })}>
          <SelectTrigger className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="amd64">x64 (amd64)</SelectItem>
            <SelectItem value="arm64">ARM64</SelectItem>
            {variant === "linux" && <SelectItem value="arm">ARM (armv7)</SelectItem>}
          </SelectContent>
        </Select>
      </div>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1.5">
        <Checkbox id={`${id}-persist`} aria-label={t("generate.panel.persist_aria")} checked={form.persist} onCheckedChange={(checked) => setForm({ ...form, persist: checked === true })} />
        <Label htmlFor={`${id}-persist`} className="text-sm text-foreground">{t("generate.panel.persist")}</Label>
        <Checkbox id={`${id}-skip-tls`} aria-label={t("generate.panel.skip_tls_aria")} checked={form.skip_tls} onCheckedChange={(checked) => setForm({ ...form, skip_tls: checked === true })} className="ml-3" />
        <Label htmlFor={`${id}-skip-tls`} className="text-sm">{t("generate.panel.skip_tls_short")}</Label>
        <Checkbox id={`${id}-obfuscate`} aria-label={t("generate.panel.obfuscate_aria")} checked={form.obfuscate} onCheckedChange={(checked) => setForm({ ...form, obfuscate: checked === true })} className="ml-3" />
        <Label htmlFor={`${id}-obfuscate`} className="text-sm">{t("generate.panel.obfuscate_aria")}</Label>
        <Checkbox id={`${id}-slim`} aria-label={t("generate.panel.slim_aria")} checked={form.slim} onCheckedChange={(checked) => setForm({ ...form, slim: checked === true })} className="ml-3" />
        <Label htmlFor={`${id}-slim`} className="text-sm">{t("generate.panel.slim")}</Label>
        <Checkbox
          id={`${id}-upx`}
          aria-label={t("generate.panel.upx_aria")}
          checked={variant === "macos" ? false : form.upx}
          disabled={variant === "macos"}
          onCheckedChange={(checked) => setForm({ ...form, upx: checked === true })}
          className="ml-3"
        />
        <Label htmlFor={`${id}-upx`} className="text-sm">
          {t("generate.panel.upx")}
          {variant === "macos" && <span className="ml-1 text-xs text-muted-foreground/80">— {t("generate.panel.upx_unsupported_macos")}</span>}
        </Label>
      </div>
      <ModuleSelector id={id} catalog={agentModules ?? []} value={form.modules} onChange={(next) => setForm({ ...form, modules: next })} slimLocked={form.slim} />
      <div>
        <FieldLabel>{t("generate.panel.domain_front")}</FieldLabel>
        <Input aria-label={t("generate.panel.domain_front_aria")} name={`${id}-domain-front`} value={form.domain_front} onChange={(e) => setForm({ ...form, domain_front: e.target.value })} placeholder={t("generate.panel.domain_front_aria")} />
      </div>
      <AdvancedSection title={t("generate.panel.working_hours")}>
        <div className="grid grid-cols-3 gap-2">
          <div>
            <FieldLabel>{t("generate.panel.start_hhmm")}</FieldLabel>
            <Input aria-label={t("generate.panel.start_aria")} type="time" value={form.working_start} onChange={(e) => setForm({ ...form, working_start: e.target.value })} />
          </div>
          <div>
            <FieldLabel>{t("generate.panel.end_hhmm")}</FieldLabel>
            <Input aria-label={t("generate.panel.end_aria")} type="time" value={form.working_end} onChange={(e) => setForm({ ...form, working_end: e.target.value })} />
          </div>
          <div>
            <FieldLabel>{t("generate.panel.timezone")}</FieldLabel>
            <Input aria-label={t("generate.panel.timezone_aria")} placeholder="UTC" value={form.working_tz} onChange={(e) => setForm({ ...form, working_tz: e.target.value })} className="font-mono text-xs" />
          </div>
        </div>
      </AdvancedSection>
    </PayloadCard>
  );
});

// ─── StagerPanel (windows / linux) ─────────────────────────────

interface StagerPanelProps {
  variant: StagerVariant;
  form: StagerForm;
  setForm: React.Dispatch<React.SetStateAction<StagerForm>>;
  busy: boolean;
  result: ReactNode;
  hint?: { code: string; text: string };
  onGenerate: () => void;
  canGenerate?: boolean;
}

const STAGER_CONFIG: Record<StagerVariant, { tint: string; icon: ReactNode; titleKey: string; subtitleKey: string }> = {
  windows: { tint: "bg-chart-6/violet text-chart-6", icon: <PackageOpen className="size-5" />, titleKey: "generate.panel.stager_win_title", subtitleKey: "generate.panel.stager_subtitle" },
  linux: { tint: "bg-chart-2/10 text-chart-2 dark:text-chart-2", icon: <Package className="size-5" />, titleKey: "generate.panel.stager_linux_title", subtitleKey: "generate.panel.stager_subtitle" },
};

export const StagerPanel = React.memo(function StagerPanel({ variant, form, setForm, busy, result, hint, onGenerate, canGenerate = true }: StagerPanelProps) {
  const { t } = useI18n();
  const cfg = STAGER_CONFIG[variant];
  const id = `stager-${variant}`;
  return (
    <PayloadCard
      icon={cfg.icon}
      tint={cfg.tint}
      title={t(cfg.titleKey)}
      subtitle={t(cfg.subtitleKey)}
      badge={<BuildStatusBadge busy={busy} result={result} />}
      footer={
        <>
          <Button type="button" onClick={onGenerate} disabled={busy || !canGenerate} title={!canGenerate ? t("generate.toast.select_listener_first") : undefined} className={BTN_CLASS}>
            {busy ? <><Spinner /> {t("generate.panel.generating")}</> : <><Download className="size-4" /> {t("generate.panel.generate_loader")}</>}
          </Button>
          <BuildResult busy={busy} result={result} hint={hint} />
        </>
      }
    >
      <div>
        <FieldLabel>{t("generate.panel.output_filename")}</FieldLabel>
        <Input aria-label={t("generate.panel.output_filename")} name={`${id}-filename`} value={form.filename} onChange={(e) => setForm({ ...form, filename: e.target.value })} />
      </div>
      <div className="flex items-center gap-x-2">
        <Checkbox id={`${id}-skip-tls`} aria-label={t("generate.panel.skip_tls_aria")} checked={form.skip_tls} onCheckedChange={(checked) => setForm({ ...form, skip_tls: checked === true })} />
        <Label htmlFor={`${id}-skip-tls`} className="text-sm text-foreground">{t("generate.panel.skip_tls")}</Label>
      </div>
    </PayloadCard>
  );
});

// ─── ShellcodePanel ────────────────────────────────────────────

export const ShellcodePanel = React.memo(function ShellcodePanel({
  form, setForm, busy, result, hint, onGenerate, canGenerate = true,
}: {
  form: ShellcodeForm;
  setForm: React.Dispatch<React.SetStateAction<ShellcodeForm>>;
  busy: boolean;
  result: ReactNode;
  hint?: { code: string; text: string };
  onGenerate: () => void;
  canGenerate?: boolean;
}) {
  const { t } = useI18n();
  return (
    <PayloadCard
      icon={<Binary className="size-5" />}
      tint="bg-chart-2/10 text-info-ink"
      title={t("generate.panel.shellcode_title")}
      subtitle={t("generate.panel.shellcode_subtitle")}
      badge={<BuildStatusBadge busy={busy} result={result} />}
      footer={
        <>
          <Button type="button" onClick={onGenerate} disabled={busy || !canGenerate} title={!canGenerate ? t("generate.toast.select_listener_first") : undefined} className={BTN_CLASS}>
            {busy ? <><Spinner /> {t("generate.panel.generating")}</> : <><Download className="size-4" /> {t("generate.panel.generate_shellcode")}</>}
          </Button>
          <BuildResult busy={busy} result={result} hint={hint} />
        </>
      }
    >
      <div>
        <FieldLabel>{t("generate.panel.command")}</FieldLabel>
        <Input aria-label={t("generate.panel.command")} name="shellcode-cmd" value={form.command} onChange={(e) => setForm({ ...form, command: e.target.value })} className="font-mono text-xs" />
      </div>
      <div>
        <FieldLabel>{t("generate.panel.filename")}</FieldLabel>
        <Input aria-label={t("generate.panel.output_filename")} name="shellcode-filename" value={form.filename} onChange={(e) => setForm({ ...form, filename: e.target.value })} />
      </div>
    </PayloadCard>
  );
});

// ─── DonutPanel ────────────────────────────────────────────────

export const DonutPanel = React.memo(function DonutPanel({
  form, setForm, busy, result, hint, onGenerate, fileRef, canGenerate = true,
}: {
  form: DonutForm;
  setForm: React.Dispatch<React.SetStateAction<DonutForm>>;
  busy: boolean;
  result: ReactNode;
  hint?: { code: string; text: string };
  onGenerate: () => void;
  fileRef: React.RefObject<HTMLInputElement | null>;
  canGenerate?: boolean;
}) {
  const { t } = useI18n();
  return (
    <PayloadCard
      icon={<Disc className="size-5" />}
      tint="bg-warning/10 text-warning-ink"
      title={t("generate.panel.donut_title")}
      subtitle={t("generate.panel.donut_subtitle")}
      badge={<BuildStatusBadge busy={busy} result={result} />}
      footer={
        <>
          <Button type="button" onClick={onGenerate} disabled={busy || !canGenerate} title={!canGenerate ? t("generate.toast.select_listener_first") : undefined} className={BTN_CLASS}>
            {busy ? <><Spinner /> {t("generate.panel.generating")}</> : <><Download className="size-4" /> {t("generate.panel.generate_donut")}</>}
          </Button>
          <BuildResult busy={busy} result={result} hint={hint} />
        </>
      }
    >
      <div>
        <FieldLabel>{t("generate.panel.dotnet_assembly")}</FieldLabel>
        <Input aria-label={t("generate.panel.upload_assembly_aria")} name="donut-assembly" ref={fileRef} type="file" accept=".exe,.dll" onChange={(e) => setForm({ ...form, assembly: e.target.files?.[0] || null })} />
      </div>
      <div>
        <FieldLabel>{t("generate.panel.arch")}</FieldLabel>
        <Select value={form.arch} onValueChange={(val) => val != null && setForm({ ...form, arch: val })}>
          <SelectTrigger className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="amd64">x64 (amd64)</SelectItem>
            <SelectItem value="x86">x86</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div>
        <FieldLabel>{t("generate.panel.class_opt")}</FieldLabel>
        <Input aria-label={t("generate.panel.blank_for_main")} name="donut-class" type="text" placeholder={t("generate.panel.blank_for_main")} value={form.class} onChange={(e) => setForm({ ...form, class: e.target.value })} className="font-mono text-xs" />
      </div>
      <div>
        <FieldLabel>{t("generate.panel.method_opt")}</FieldLabel>
        <Input aria-label={t("generate.panel.main_aria")} name="donut-method" type="text" placeholder={t("generate.panel.main_aria")} value={form.method} onChange={(e) => setForm({ ...form, method: e.target.value })} className="font-mono text-xs" />
      </div>
      <div>
        <FieldLabel>{t("generate.panel.args_opt")}</FieldLabel>
        <Input aria-label={t("generate.panel.args_opt")} name="donut-args" type="text" placeholder="" value={form.args} onChange={(e) => setForm({ ...form, args: e.target.value })} className="font-mono text-xs" />
      </div>
      <div>
        <FieldLabel>{t("generate.panel.output_filename")}</FieldLabel>
        <Input aria-label={t("generate.panel.output_filename")} name="donut-filename" value={form.filename} onChange={(e) => setForm({ ...form, filename: e.target.value })} />
      </div>
    </PayloadCard>
  );
});

// ─── PS1Panel ──────────────────────────────────────────────────

export const PS1Panel = React.memo(function PS1Panel({
  form, setForm, busy, result, hint, code, originalLen, obfuscatedLen, onGenerate, canGenerate = true,
}: {
  form: PS1Form;
  setForm: React.Dispatch<React.SetStateAction<PS1Form>>;
  busy: boolean;
  result: ReactNode;
  hint?: { code: string; text: string };
  code?: string;
  originalLen?: number;
  obfuscatedLen?: number;
  onGenerate: () => void;
  canGenerate?: boolean;
}) {
  const { t } = useI18n();
  return (
    <PayloadCard
      icon={<Terminal className="size-5" />}
      tint="bg-primary/10 text-primary"
      title={t("generate.panel.ps1_title")}
      subtitle={t("generate.panel.ps1_subtitle")}
      badge={<BuildStatusBadge busy={busy} result={result} />}
      footer={
        <>
          <Button type="button" onClick={onGenerate} disabled={busy || !canGenerate} title={!canGenerate ? t("generate.toast.select_listener_first") : undefined} className={BTN_CLASS}>
            {busy ? <><Spinner /> {t("generate.panel.generating")}</> : <><Wand2 className="size-4" /> {t("generate.panel.generate_ps1")}</>}
          </Button>
          <BuildResult busy={busy} result={result} hint={hint} />
        </>
      }
    >
      <div>
        <FieldLabel>{t("generate.panel.filename")}</FieldLabel>
        <Input id="ps1-filename" placeholder="agent.ps1" value={form.filename} onChange={(e) => setForm({ ...form, filename: e.target.value })} />
      </div>
      <div className="flex items-center gap-x-2">
        <Checkbox id="ps1-persist" aria-label={t("generate.panel.persist")} checked={form.persist} onCheckedChange={(checked) => setForm({ ...form, persist: checked === true })} />
        <Label htmlFor="ps1-persist" className="text-sm text-foreground">{t("generate.panel.persist")}</Label>
      </div>
      <div className="flex items-center gap-x-2">
        <Checkbox id="ps1-skip-tls" aria-label={t("generate.panel.skip_tls_aria")} checked={form.skip_tls} onCheckedChange={(checked) => setForm({ ...form, skip_tls: checked === true })} />
        <Label htmlFor="ps1-skip-tls" className="text-sm">{t("generate.panel.skip_tls")}</Label>
      </div>
      {code ? (
        <div className="mt-1">
          <div className="mb-2 flex items-center justify-between">
            <span className="flex items-center gap-x-1.5 text-xs font-medium text-success-ink">
              <CheckCircle2 className="size-4" /> {t("generate.panel.generated_sizes", { original: originalLen ?? 0, obfuscated: obfuscatedLen ?? 0 })}
            </span>
            <CopyButton text={code} label={t("generate.panel.copy")} size="xs" />
          </div>
          <Textarea aria-label={t("generate.panel.ps1_title")} name="ps1-output" readOnly value={code} className="h-48 resize-none bg-background p-3 font-mono text-xs text-chart-1 border-border" />
          <div className="mt-1 flex items-center gap-x-1.5 text-xs text-muted-foreground"><Info className="size-4" /> {t("generate.panel.paste_ps")}</div>
        </div>
      ) : null}
    </PayloadCard>
  );
});
