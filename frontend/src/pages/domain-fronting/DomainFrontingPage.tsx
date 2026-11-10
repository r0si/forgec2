
import { registerFragment } from "@/lib/i18n";
import { enDomainFronting } from "@/lib/i18n/fragments/en.domain_fronting";
registerFragment(enDomainFronting);

import { useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { useApiResource } from "@/lib/hooks/useApiResource";
import { useMutation } from "@/lib/hooks/useMutation";
import { POLL } from "@/lib/polling";
import { DataError, DataSpinner } from "@/components/ui/data-state";
import { EmptyState } from "@/components/ui/empty-state";
import { FieldError } from "@/components/ui/field-error";
import { PageContainer } from "@/components/ui/page-container";
import { CardHeaderRow } from "@/components/ui/card-header-row";
import { toast } from "sonner";
import { formatTime } from "@/lib/utils";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Cloud, HeartPulse, Info, Plus, RotateCw, Trash2 } from "lucide-react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

interface FrontDomain {
  domain: string;
  healthy: boolean;
  active: boolean;
  last_check: string;
  error?: string;
}

const HOSTNAME_RE = /^(?!.*\/)(?!.*:)[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?)+$/;

export default function DomainFrontingPage() {
  const { t } = useI18n();
  const [checking, setChecking] = useState(false);
  const [newDomain, setNewDomain] = useState("");
  const [domainError, setDomainError] = useState("");
  const { confirm, modal } = useConfirm();

  const { mutate: saveConfig, isPending: saving } = useMutation({
    fn: async (cfgDomains: string[], cfgAuto: boolean) => {
      await api.postJson(paths.domainFront.config, { domains: cfgDomains, auto_failover: cfgAuto });
    },
    onSuccess: () => {
      toast.success(t("domain_fronting.toast.config_saved"));
      fetchStatus();
    },
    onError: () => toast.error(t("domain_fronting.toast.config_save_failed")),
  });

  const { data, loading, error, refresh: fetchStatus } = useApiResource<{ domains: FrontDomain[]; auto_failover?: boolean }>({
    fetcher: async () => {
      const data = await api.get(paths.domainFront.list);
      return data as { domains: FrontDomain[]; auto_failover?: boolean };
    },
    toastThrottleMs: POLL.toastThrottle,
    errorMessage: t("domain_fronting.toast.fetch_status_failed"),
  });
  const domains = data?.domains ?? [];
  const autoFailover = data?.auto_failover ?? true;
  // Every mutator below POSTs the whole domain list, so an unread payload must
  // not be treated as "no domains" — that would wipe the real configuration.
  const configUnread = loading || !!error;

  const guardUnread = (): boolean => {
    if (!configUnread) return false;
    toast.error(t("domain_fronting.config_unread"));
    return true;
  };

  const handleCheck = async () => {
    setChecking(true);
    try {
      await api.postJson(paths.domainFront.check, {});
      toast.success(t("domain_fronting.toast.health_check_ok"));
      fetchStatus();
    } catch {
      toast.error(t("domain_fronting.toast.health_check_failed"));
    } finally {
      setChecking(false);
    }
  };

  const addDomain = () => {
    if (guardUnread()) return;
    const d = newDomain.trim();
    if (!d || !HOSTNAME_RE.test(d)) {
      setDomainError(t("domain_fronting.domain_invalid"));
      return;
    }
    if (domains.some((x) => x.domain === d)) {
      toast.error(t("domain_fronting.toast.already_in_list"));
      return;
    }
    const updated = [...domains.map((x) => x.domain), d];
    saveConfig(updated, autoFailover);
    setNewDomain("");
    setDomainError("");
  };

  const removeDomain = async (domain: string) => {
    if (guardUnread()) return;
    if (!(await confirm({ message: t("domain_fronting.remove_domain", { domain }) }))) return;
    const updated = domains.filter((x) => x.domain !== domain).map((x) => x.domain);
    saveConfig(updated, autoFailover);
  };

  const toggleAutoFailover = () => {
    if (guardUnread()) return;
    saveConfig(domains.map((x) => x.domain), !autoFailover);
  };

  return (
    <PageContainer title={t("domain_fronting.title")} icon={<Cloud className="size-4" />} subtitle={t("domain_fronting.subtitle")} actions={<>
        <Button onClick={handleCheck} disabled={checking || loading} size="sm">
          <HeartPulse className={`size-4 ${checking ? "animate-pulse" : ""}`} />
          {checking ? t("domain_fronting.checking") : t("domain_fronting.health_check")}
        </Button>
        <Button onClick={fetchStatus} variant="ghost" size="sm">
          <RotateCw className="size-4" /> {t("common.refresh")}
        </Button>
      </>}>

      {error && <DataError message={error} onRetry={fetchStatus} className="mb-4" />}

      {/* Auto-failover toggle */}
      <Card className="p-(--card-spacing) flex items-center justify-between">
        <div>
          <div className="font-medium text-foreground">{t("domain_fronting.auto_failover")}</div>
          <div className="text-sm text-muted-foreground mt-0.5">
            {t("domain_fronting.auto_failover_desc")}
          </div>
        </div>
        <Switch checked={autoFailover} onCheckedChange={toggleAutoFailover} disabled={saving || configUnread} />
      </Card>

      {/* Active domain indicator */}
      <Card className="p-(--card-spacing)">
        <div className="text-sm text-muted-foreground mb-1">{t("domain_fronting.current_active_domain")}</div>
        <div className="flex items-center gap-3">
          {configUnread ? (
            <span className="text-sm text-muted-foreground">{t("status.unknown")}</span>
          ) : domains.filter((d) => d.active).length > 0 ? (
            <span className="text-lg font-semibold text-foreground">
              {domains.find((d) => d.active)?.domain}
            </span>
          ) : (
            <span className="text-sm text-muted-foreground">{t("domain_fronting.no_domain")}</span>
          )}
          {domains.filter((d) => d.active && d.healthy).length > 0 && (
            <Badge variant="success">
              {t("domain_fronting.healthy")}
            </Badge>
          )}
        </div>
      </Card>

      {/* Domain list */}
      <Card className="overflow-hidden">
        <CardHeaderRow accent={false} title={t("domain_fronting.front_domains")} action={configUnread ? <span className="text-xs text-muted-foreground">{t("status.unknown")}</span> : <span className="text-xs text-muted-foreground">{t("domain_fronting.domains_count", { count: domains.length })}</span>} />

        {loading ? (
          <div className="p-(--card-spacing)">
            <DataSpinner message={t("common.loading")} />
          </div>
        ) : error ? (
          <DataError message={error} onRetry={fetchStatus} />
        ) : domains.length === 0 ? (
          <EmptyState icon={Cloud} title={t("domain_fronting.empty_title")} message={t("domain_fronting.empty_message")} />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("domain_fronting.col_domain")}</TableHead>
                <TableHead>{t("domain_fronting.col_status")}</TableHead>
                <TableHead className="w-12"></TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {domains.map((d) => (
                <TableRow key={d.domain}>
                  <TableCell>
                    <div className="flex items-center gap-3 min-w-0">
                      <Tooltip>
                        <TooltipTrigger>
                          <span
                            className={`size-2.5 rounded-full shrink-0 ${
                              d.healthy ? "bg-success" : "bg-destructive"
                            } ${d.active ? "animate-pulse" : ""}`}
                          />
                        </TooltipTrigger>
                        <TooltipContent>{d.healthy ? t("domain_fronting.healthy") : t("domain_fronting.unhealthy")}</TooltipContent>
                      </Tooltip>
                      <div className="min-w-0">
                        <div className="flex items-center gap-2">
                          <span className="font-medium text-foreground truncate">{d.domain}</span>
                          {d.active && (
                            <Badge variant="outline" className="text-micro-sm px-1.5 py-px font-medium">{t("domain_fronting.active")}
                            </Badge>
                          )}
                        </div>
                        <div className="text-xs text-muted-foreground mt-0.5">
                          {d.last_check ? (
                            <>{t("domain_fronting.last_checked")}: {formatTime(d.last_check)}</>
                          ) : (
                            t("domain_fronting.not_checked")
                          )}
                          {d.error && (
                            <span className="text-destructive-ink ml-2">{t("domain_fronting.error_prefix")} {d.error}</span>
                          )}
                        </div>
                      </div>
                    </div>
                  </TableCell>
                  <TableCell>
                    <Badge variant="outline" className={`${
                      d.healthy
                        ? "bg-success/15 dark:bg-success/20 text-success-ink dark:text-success-ink border-success dark:border-success"
                        : "bg-destructive/10 text-destructive-ink border-destructive/20"
                    }`}>
                      {d.healthy ? t("domain_fronting.healthy") : t("domain_fronting.unhealthy")}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <Tooltip>
                      <TooltipTrigger render={<Button
                          variant="ghost"
                          size="sm"
                          onClick={() => removeDomain(d.domain)}
                          className="text-muted-foreground hover:text-destructive-ink hover:bg-destructive/10"
                        />}>
                        <Trash2 className="size-4" />
                      </TooltipTrigger>
                      <TooltipContent>{t("domain_fronting.remove_tooltip")}</TooltipContent>
                    </Tooltip>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}

        {/* Add domain form */}
        <div className="px-4 py-3 border-t border-border bg-muted/50">
          {error && (
            <p className="mb-2 text-xs text-destructive-ink">{t("domain_fronting.config_unread")}</p>
          )}
          <div className="flex items-center gap-2">
            <Input
              aria-label={t("domain_fronting.domain_ph")}
              name="input-0"
              type="text"
              value={newDomain}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => { setNewDomain(e.target.value); if (domainError) setDomainError(""); }}
              onKeyDown={(e: React.KeyboardEvent<HTMLInputElement>) => e.key === "Enter" && addDomain()}
              placeholder={t("domain_fronting.domain_ph")}
              disabled={configUnread}
              className="flex-1"
            />
            <Button
              onClick={addDomain}
              disabled={!newDomain.trim() || saving || configUnread}
            >
              <Plus className="size-4" />
              {t("domain_fronting.add")}
            </Button>
          </div>
          <p className="text-xs text-muted-foreground mt-1.5">
            {t("domain_fronting.enter_hint_pre")} <code className="text-primary">cdn.cloudflare.com</code>{t("domain_fronting.enter_hint_post")}
          </p>
          <FieldError>{domainError}</FieldError>
        </div>
      </Card>

      {/* Info card */}
      <div className="mt-6 p-4 bg-warning/10 border border-warning/30 rounded-xl">
        <div className="flex items-start gap-3">
          <Info className="size-4" />
          <div className="text-sm text-warning-ink dark:text-warning-ink">
            <strong>{t("domain_fronting.how_it_works")}</strong> {t("domain_fronting.monitor_head")}{" "}
            <code className="text-warning-ink dark:text-warning-ink">/api/v1/beacon</code>{t("domain_fronting.monitor_tail")}
          </div>
        </div>
      </div>
      {modal}
    </PageContainer>
  );
}
