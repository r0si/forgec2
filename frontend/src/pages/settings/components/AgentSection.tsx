import { AgentForm } from "./types";
import { Card } from "@/components/ui/card";
import { CardHeaderRow } from "@/components/ui/card-header-row";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import Toggle from "./Toggle";
import { Bot, Save } from "lucide-react";
import { useI18n } from "@/lib/i18n";
import { Field } from "@/components/ui/field";

export default function AgentSection({
  form, setForm, saving, onSave, settingsLoaded,
}: {
  form: AgentForm;
  setForm: React.Dispatch<React.SetStateAction<AgentForm>>;
  saving: boolean;
  onSave: (e: React.FormEvent) => void;
  settingsLoaded: boolean;
}) {
  const { t } = useI18n();
  return (
    <Card className="overflow-hidden">
      <CardHeaderRow icon={Bot} tone="success" title={t("settings.agent.title")} description={t("settings.agent.subtitle")} />
      <div className="p-(--card-spacing)">
        <form onSubmit={onSave} className="space-y-4">
          <div className="grid grid-cols-2 md:grid-cols-3 gap-4">
            <Field label={t("settings.agent.heartbeat_interval")} htmlFor="agent-heartbeat-interval">
              <Input id="agent-heartbeat-interval" type="number" min={0} max={300} value={form.interval} onChange={(e) => setForm({ ...form, interval: Number(e.target.value) })}  />
              <p className="text-micro-sm text-muted-foreground mt-1">{t("settings.agent.heartbeat_hint")}</p>
            </Field>
            <Field label={t("settings.agent.jitter")} htmlFor="agent-jitter">
              <Input id="agent-jitter" type="number" min={0} max={100} value={form.jitter} onChange={(e) => setForm({ ...form, jitter: Number(e.target.value) })}  />
            </Field>
            <div>
              <span className="block text-xs text-muted-foreground mb-1.5">{t("settings.agent.skip_tls")}</span>
              <div className="h-11 flex items-center gap-3">
                <Toggle checked={form.skip_tls} onChange={(v) => setForm({ ...form, skip_tls: v })} />
                <span className="text-sm text-muted-foreground">{form.skip_tls ? t("settings.agent.enabled") : t("settings.agent.disabled")}</span>
              </div>
            </div>
          </div>
          <Field label={t("settings.agent.user_agent")} htmlFor="agent-user-agent">
              <Input id="agent-user-agent" type="text" value={form.user_agent} onChange={(e) => setForm({ ...form, user_agent: e.target.value })} className="font-mono" />
          </Field>
          <div className="border-t pt-4">
            <h3 className="text-sm font-medium text-muted-foreground mb-3">{t("settings.agent.working_hours")}</h3>
            <div className="grid grid-cols-2 md:grid-cols-3 gap-4">
              <Field label={t("settings.agent.working_start")} htmlFor="agent-working-start">
                <Input id="agent-working-start" type="time" value={form.working_start} onChange={(e) => setForm({ ...form, working_start: e.target.value })}  />
              </Field>
              <Field label={t("settings.agent.working_end")} htmlFor="agent-working-end">
                <Input id="agent-working-end" type="time" value={form.working_end} onChange={(e) => setForm({ ...form, working_end: e.target.value })}  />
              </Field>
              <Field label={t("settings.agent.timezone")} htmlFor="agent-working-tz">
                <Input id="agent-working-tz" type="text" placeholder="UTC" value={form.working_tz} onChange={(e) => setForm({ ...form, working_tz: e.target.value })} className="font-mono" />
                <p className="text-micro-sm text-muted-foreground mt-1">{t("settings.agent.tz_hint")}</p>
              </Field>
            </div>
            <p className="text-micro-sm text-muted-foreground mt-2">{t("settings.agent.working_hint")}</p>
          </div>
          <div className="border-t pt-4">
            <h3 className="text-sm font-medium text-muted-foreground mb-3">{t("settings.agent.rand_title")}</h3>
            <div className="grid grid-cols-2 md:grid-cols-3 gap-4">
              <Field label={t("settings.agent.rand_mode")} htmlFor="agent-beacon-randomization">
                <Select value={form.beacon_randomization} onValueChange={(v) => v && setForm({ ...form, beacon_randomization: v })}>
                  <SelectTrigger id="agent-beacon-randomization" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="linear">{t("generate.rand_linear")}</SelectItem>
                    <SelectItem value="gaussian">{t("generate.rand_gaussian")}</SelectItem>
                    <SelectItem value="exponential">{t("generate.rand_exponential")}</SelectItem>
                    <SelectItem value="two_stage">{t("generate.rand_two_stage")}</SelectItem>
                    <SelectItem value="adaptive">{t("generate.rand_adaptive")}</SelectItem>
                  </SelectContent>
                </Select>
                <p className="text-micro-sm text-muted-foreground mt-1">{t("settings.agent.rand_hint")}</p>
              </Field>
              <Field label={t("settings.agent.max_rand")} htmlFor="agent-max-random-ms">
                <Input id="agent-max-random-ms" type="number" min={0} max={600000} step={100} value={form.max_random_interval_ms} onChange={(e) => setForm({ ...form, max_random_interval_ms: Math.min(600000, Math.max(0, Number(e.target.value) || 0)) })} />
                <p className="text-micro-sm text-muted-foreground mt-1">{t("settings.agent.max_rand_hint")}</p>
              </Field>
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <Button type="submit" size="lg" disabled={saving || !settingsLoaded} className="px-6 text-sm font-medium transition-colors disabled:opacity-50">
              <Save className="size-4" />{t("settings.agent.save")}
            </Button>
            {!settingsLoaded && <span className="text-xs text-muted-foreground">{t("settings.unread_save_blocked")}</span>}
          </div>
        </form>
      </div>
    </Card>
  );
}