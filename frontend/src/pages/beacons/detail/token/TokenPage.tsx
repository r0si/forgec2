import { registerFragment } from "@/lib/i18n";
import { enAgents } from "@/lib/i18n/fragments/en.agents";
registerFragment(enAgents);

import { useState, useEffect, useCallback, useRef } from "react";
import { useParams } from "react-router-dom";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { errorText } from "@/lib/errors";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "sonner";
import { Label } from "@/components/ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Spinner } from "@/components/ui/spinner";
import { PageContainer } from "@/components/ui/page-container";
import { AlertCircle, AlertTriangle, BadgeInfo, Check, CircleDot, Info, Key, List, Pencil, Plus, PlusCircle, RotateCcw, RotateCw, Trash2, User, UserCheck, UserCog, X } from "lucide-react";
import { EmptyState } from "@/components/ui/empty-state";
import { Card } from "@/components/ui/card";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useI18n } from "@/lib/i18n";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { useMutation } from "@/lib/hooks/useMutation";
import { useErrorToast } from "@/lib/hooks/useErrorToast";
import { formatTime } from "@/lib/utils";
import { Field } from "@/components/ui/field";

interface Token {
  id?: number;
  ID?: string;
  Domain?: string;
  domain?: string;
  Username?: string;
  username?: string;
  Integrity?: string;
  integrity?: string;
  Source?: string;
  source?: string;
  PID?: number;
  pid?: number;
  ProcessName?: string;
  process_name?: string;
  Active?: boolean;
  active?: boolean;
  CreatedAt?: string;
  created_at?: string;
  TokenType?: string;
  token_type?: string;
  Protocol?: string;
  protocol?: string;
  Note?: string;
  note?: string;
}

interface Process {
  pid: number;
  name: string;
  user?: string;
}


export default function AgentTokenPage() {
  const { t } = useI18n();
  const { toastError } = useErrorToast();
  const { confirm, modal } = useConfirm();
  const params = useParams();
  const agentId = params.id as string;
  const [tokens, setTokens] = useState<Token[]>([]);
  const [processes, setProcesses] = useState<Process[]>([]);
  const [loading, setLoading] = useState(true);
  const [tokenError, setTokenError] = useState<string | null>(null);
  const [processError, setProcessError] = useState<string | null>(null);
  const [stealPid, setStealPid] = useState("");
  const [makeUser, setMakeUser] = useState("");
  const [makeDomain, setMakeDomain] = useState("");
  const [makePass, setMakePass] = useState("");
  const [activeAction, setActiveAction] = useState<string | null>(null);

  const [whoamiResult, setWhoamiResult] = useState<string | null>(null);
  const [whoamiError, setWhoamiError] = useState(false);
  const [tokenNotes, setTokenNotes] = useState<Record<string, string>>({});
  const [noteTargetId, setNoteTargetId] = useState<string | null>(null);
  const tokenLoadSeqRef = useRef(0);
  const processLoadSeqRef = useRef(0);
  const delayedReloadRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const loadTokens = useCallback(async (signal?: AbortSignal) => {
    const seq = ++tokenLoadSeqRef.current;
    setTokenError(null);
    try {
      // B1 fix: backend returns a raw array, not {tokens: [...]}. Handle both
      // formats for forward compatibility.
      const res = await api.get(paths.agents.tokenList(agentId), { signal });
      const list = Array.isArray(res) ? res : ((res as Record<string, unknown>)?.tokens || (res as Record<string, unknown>)?.Tokens || []) as Token[];
      if (signal?.aborted || seq !== tokenLoadSeqRef.current) return;
      setTokens(list as Token[]);
    } catch (err) {
      if (signal?.aborted || seq !== tokenLoadSeqRef.current) return;
      // Keep the last good list; only the error flag changes so the UI can
      // report the failure instead of claiming "no tokens harvested yet".
      setTokenError(errorText(t, err, () => t("agents.token_load_failed")));
    }
  }, [agentId, t]);

  const loadProcesses = useCallback(async (signal?: AbortSignal) => {
    const seq = ++processLoadSeqRef.current;
    setProcessError(null);
    try {
      const data = await api.post<{ processes?: Process[]; data?: Process[] }>(paths.agents.tokenListProcs(agentId), undefined, { signal });
      if (signal?.aborted || seq !== processLoadSeqRef.current) return;
      setProcesses(data.processes || data.data || []);
    } catch (err) {
      if (signal?.aborted || seq !== processLoadSeqRef.current) return;
      setProcessError(errorText(t, err, () => t("agents.token_load_processes_failed")));
    }
  }, [agentId, t]);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    Promise.all([loadTokens(controller.signal), loadProcesses(controller.signal)])
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [loadTokens, loadProcesses]);

  const scheduleTokenReload = useCallback(() => {
    if (delayedReloadRef.current) clearTimeout(delayedReloadRef.current);
    delayedReloadRef.current = setTimeout(() => {
      delayedReloadRef.current = null;
      void loadTokens();
    }, 3000);
  }, [loadTokens]);

  useEffect(() => () => {
    if (delayedReloadRef.current) clearTimeout(delayedReloadRef.current);
  }, [agentId]);

  // Each operator action owns its error routing (normalized message) and
  // stale-completion guard; `activeAction` stays local because the spinners
  // name the exact row/action.
  const { mutate: stealToken, reset: resetSteal } = useMutation({
    fn: (pid: string) => {
      const body = new URLSearchParams();
      body.append("pid", pid);
      return api.post(paths.agents.tokenSteal(agentId), Object.fromEntries(body.entries()));
    },
    onSuccess: () => {
      setStealPid("");
      // B7 fix: task accepted, poll later
      toast.info(t("agents.token_task_queued"));
      scheduleTokenReload();
    },
    onError: (e) => toastError(e, "agents.token_steal_failed"),
  });

  const { mutate: makeToken, reset: resetMake } = useMutation({
    fn: () => {
      const body = new URLSearchParams();
      // B2 fix: backend reads "user" not "username"
      body.append("user", makeUser);
      body.append("domain", makeDomain);
      body.append("password", makePass);
      return api.post(paths.agents.tokenMake(agentId), Object.fromEntries(body.entries()));
    },
    onSuccess: () => {
      setMakeUser(""); setMakeDomain(""); setMakePass("");
      // B7 fix: token operations only return task_id, the actual token appears
      // later via agent callback. Show "task accepted" instead of false success.
      toast.info(t("agents.token_task_queued"));
      scheduleTokenReload();
    },
    onError: (e) => toastError(e, "agents.token_make_failed"),
  });

  const { mutate: revertToken, reset: resetRevert } = useMutation({
    fn: () => api.post(paths.agents.tokenRevert(agentId)),
    onSuccess: () => {
      toast.info(t("agents.token_task_queued"));
      setWhoamiResult(null);
      setWhoamiError(false);
      scheduleTokenReload();
    },
    onError: (e) => toastError(e, "agents.token_revert_failed"),
  });

  const { mutate: dropToken, reset: resetDrop } = useMutation({
    fn: (tokenId: string) => api.del(paths.agents.tokenOne(agentId, tokenId)),
    onSuccess: () => {
      toast.success(t("agents.token_drop_success"));
      void loadTokens();
    },
    onError: (e) => toastError(e, "agents.token_drop_failed"),
  });

  const { mutate: impersonateToken, reset: resetImpersonate } = useMutation({
    fn: (tokenId: string) => api.post(paths.agents.tokenImpersonate(agentId, tokenId)),
    onSuccess: () => {
      toast.success(t("agents.token_impersonate"));
      void loadTokens();
    },
    onError: (e) => toastError(e, "agents.token_impersonate_failed"),
  });

  const { mutate: queryWhoami, reset: resetWhoami } = useMutation({
    fn: () => api.post(paths.agents.tokenWhoami(agentId)),
    onSuccess: (data) => {
      const d = data as Record<string, unknown>;
      const user = String(d.user || d.username || d.name || JSON.stringify(data));
      setWhoamiResult(user);
      setWhoamiError(false);
      toast(`Identity: ${d.user || d.username || t("agents.unknown")}`);
    },
    onError: (e) => {
      const normalized = toastError(e, "agents.token_whoami_failed");
      setWhoamiResult(normalized.message || t("agents.token_whoami_failed"));
      setWhoamiError(true);
    },
  });

  const { mutate: saveNote, reset: resetNote } = useMutation({
    fn: (tokenId: string, text: string) => {
      const body = new URLSearchParams();
      // B3 fix: backend reads "notes" not "note"
      body.append("notes", text);
      return api.post(paths.agents.tokenNote(agentId, tokenId), Object.fromEntries(body.entries()));
    },
    onSuccess: (_data, _tokenId, text) => {
      toast.success(text ? t("agents.token_notes_saved") : t("agents.token_notes_cleared"));
      void loadTokens();
    },
    onError: (e) => toastError(e, "agents.token_notes_error"),
  });

  // Route reuse: /beacons/A/token → /beacons/B/token keeps the page mounted. The
  // old agent's drafts, identity banner and in-flight actions must not carry
  // over — a stale completion would toast for the wrong agent.
  useEffect(() => {
    setStealPid("");
    setMakeUser("");
    setMakeDomain("");
    setMakePass("");
    setActiveAction(null);
    setWhoamiResult(null);
    setWhoamiError(false);
    setTokenNotes({});
    setNoteTargetId(null);
    resetSteal();
    resetMake();
    resetRevert();
    resetDrop();
    resetImpersonate();
    resetWhoami();
    resetNote();
  }, [agentId, resetSteal, resetMake, resetRevert, resetDrop, resetImpersonate, resetWhoami, resetNote]);

  const handleStealToken = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!stealPid) return;
    // Stealing a token injects into another process: confirm the PID first,
    // a typo here targets the wrong process.
    const okSteal = await confirm({ title: t("agents.token_steal_title"), message: t("agents.token_steal_confirm", { pid: stealPid }), danger: true });
    if (!okSteal) return;
    setActiveAction("steal");
    try {
      await stealToken(stealPid);
    } finally {
      setActiveAction(null);
    }
  };

  const handleMakeToken = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!makeUser) return;
    setActiveAction("make");
    try {
      await makeToken();
    } finally {
      setActiveAction(null);
    }
  };

  const handleRevert = async () => {
    setActiveAction("revert");
    try {
      await revertToken();
    } finally {
      setActiveAction(null);
    }
  };

  const handleDrop = async (tokenId: string) => {
    const ok = await confirm({ title: t("agents.token_drop_title"), message: t("agents.token_drop_confirm"), danger: true });
    if (!ok) return;
    setActiveAction(`drop-${tokenId}`);
    try {
      await dropToken(tokenId);
    } finally {
      setActiveAction(null);
    }
  };

  const handleImpersonate = async (tokenId: string) => {
    // Impersonation changes the security context the agent operates under:
    // confirm explicitly, like the destructive drop action below.
    const okImp = await confirm({ title: t("agents.token_impersonate_title"), message: t("agents.token_impersonate_confirm"), danger: true });
    if (!okImp) return;
    setActiveAction(`impersonate-${tokenId}`);
    try {
      await impersonateToken(tokenId);
    } finally {
      setActiveAction(null);
    }
  };

  const handleWhoami = async () => {
    setActiveAction("whoami");
    try {
      await queryWhoami();
    } finally {
      setActiveAction(null);
    }
  };

  const handleNote = async (tokenId: string) => {
    const noteText = tokenNotes[tokenId] || "";
    setActiveAction(`note-${tokenId}`);
    try {
      await saveNote(tokenId, noteText);
    } finally {
      setActiveAction(null);
      setNoteTargetId(null);
    }
  };

  const getIntegrityBadge = (integrity: string) => {
    const variants: Record<string, "destructive" | "default" | "secondary" | "outline"> = {
      System: "destructive",
      High: "destructive",
      Medium: "secondary",
      Low: "outline",
    };
    return <Badge variant={variants[integrity] || "secondary"}>{integrity}</Badge>;
  };

  const getTokenTypeBadge = (source: string, tokenType?: string, protocol?: string) => {
    const type = tokenType || protocol || source;
    const icons: Record<string, React.ReactNode> = {
      steal: <UserCog className="size-3" />,
      make: <PlusCircle className="size-3" />,
      named_pipe: <CircleDot className="size-3" />,
      impersonate: <UserCog className="size-3" />,
      create: <Key className="size-3" />,
    };
    const icon = icons[type] || <BadgeInfo className="size-3" />;
    const labels: Record<string, string> = {
      steal: t("agents.token_src_steal"),
      make: t("agents.token_src_make"),
      named_pipe: t("agents.token_src_named_pipe"),
      impersonate: t("agents.token_src_impersonate"),
      create: t("agents.token_src_create"),
    };
    const label = labels[type] || type;
    return (
      <Badge variant={source === "steal" ? "destructive" : "default"}>
        {icon} {label}
      </Badge>
    );
  };  return (
    <PageContainer
      title={t("nav.beacon_token")}
      subtitle={t("agents.token_subtitle", { hostname: agentId.substring(0, 12) })}
      icon={<BadgeInfo className="size-4" />}
      actions={
        <>
          <Button variant="outline" size="sm" onClick={handleWhoami} disabled={activeAction === "whoami"}>
            {activeAction === "whoami" ? (
              <><Spinner size="sm" /> {t("agents.token_checking")}</>
            ) : (
              <><UserCheck className="size-4" /> {t("agents.token_whoami")}</>
            )}
          </Button>
          <Button variant="outline" size="sm" onClick={() => { setLoading(true); Promise.all([loadTokens(), loadProcesses()]).finally(() => setLoading(false)); }}>
            <RotateCw className="size-4" /> {t("common.refresh")}
          </Button>
        </>
      }
    >

      {whoamiResult && (
        <div className={`border rounded-lg px-4 py-3 text-sm flex items-center gap-2 ${
          whoamiError
            ? "border-destructive/30 bg-destructive/10 text-destructive-ink"
            : "border-primary/20 bg-primary/10 dark:border-primary/40 dark:bg-primary/20 text-primary"
        }`}>
          {whoamiError ? <AlertCircle className="size-4" /> : <UserCheck className="size-4" />}
          <span>{t("agents.token_current_context")} <strong>{whoamiResult}</strong></span>
          <Button variant="ghost" size="sm" onClick={() => setWhoamiResult(null)} className="ml-auto opacity-60 hover:opacity-100">
            <X className="size-4" />
          </Button>
        </div>
      )}

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
        <Card className="p-(--card-spacing)">
          <h2 className="text-sm font-semibold text-foreground mb-3">            <User className="size-4" />{t("agents.token_steal")}
          </h2>
          <form onSubmit={handleStealToken} className="space-y-3">
            <Field label={t("agents.token_pid")} htmlFor="steal-pid">
              <Select value={stealPid} onValueChange={(v) => setStealPid(v ?? "")} disabled={!!processError}>
                <SelectTrigger id="steal-pid" className="w-full">
                  <SelectValue placeholder={processError ? t("agents.token_processes_unreadable") : t("agents.token_steal")} />
                </SelectTrigger>
                <SelectContent>
                  {processes.map((p) => (
                    <SelectItem key={p.pid} value={String(p.pid)}>[{p.pid}] {p.name}{p.user ? ` (${p.user})` : ""}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {processError && (
                <div role="alert" className="mt-1.5 flex items-center gap-2 text-micro-sm text-warning-ink">
                  <AlertTriangle className="size-3 shrink-0" aria-hidden="true" />
                  <span className="min-w-0 flex-1">{t("agents.token_load_processes_failed")}</span>
                  <Button type="button" onClick={() => { void loadProcesses(); }} size="xs" variant="outline">{t("common.try_again")}</Button>
                </div>
              )}
            </Field>
            <Button type="submit" disabled={!stealPid || activeAction === "steal"} className="w-full" variant="default">
              {activeAction === "steal" ? <><Spinner size="sm" />{t("agents.token_stealing")}</> : <><User className="size-4" />{t("agents.token_steal")}</>}
            </Button>
          </form>
        </Card>

        <Card className="p-(--card-spacing)">          <h2 className="text-sm font-semibold text-foreground mb-3">            <PlusCircle className="size-4" />{t("agents.token_make")}
          </h2>
          <form onSubmit={handleMakeToken} className="space-y-3">
            <div>
              <Label htmlFor="make-user" className="block mb-1.5">{t("agents.token_username")} <span className="text-destructive-ink">*</span></Label>
              <Input id="make-user" type="text" value={makeUser} onChange={(e) => setMakeUser(e.target.value)} placeholder={t("agents.token_username")} />
            </div>
            <Field label={t("agents.token_domain")} htmlFor="make-domain">
              <Input id="make-domain" type="text" value={makeDomain} onChange={(e) => setMakeDomain(e.target.value)} placeholder={t("agents.token_domain")} />
            </Field>
            <Field label={t("agents.token_password")} htmlFor="make-pass">
              <Input id="make-pass" type="password" value={makePass} onChange={(e) => setMakePass(e.target.value)} placeholder={t("agents.token_password")} />
            </Field>
            <Button type="submit" disabled={!makeUser || activeAction === "make"} className="w-full" variant="default">
              {activeAction === "make" ? <><Spinner size="sm" />{t("agents.token_creating")}</> : <><Plus className="size-4" />{t("agents.token_make")}</>}
            </Button>
          </form>
        </Card>        <Card className="p-(--card-spacing)">
          <h2 className="text-sm font-semibold text-foreground mb-3">
            <RotateCcw className="size-4" />{t("agents.token_quick_actions")}          </h2>
          <div className="space-y-3">
            <Button onClick={handleRevert} disabled={activeAction === "revert"} className="w-full" variant="default">
              {activeAction === "revert" ? <><Spinner size="sm" />{t("agents.token_reverting")}</> : <><RotateCcw className="size-4" />{t("agents.token_revert")}</>}
            </Button>
            <Button onClick={handleWhoami} disabled={activeAction === "whoami"} className="w-full" variant="secondary">
              {activeAction === "whoami" ? <><Spinner size="sm" />{t("agents.token_querying")}</> : <><UserCheck className="size-4" />{t("agents.token_whoami")}</>}
            </Button>
            <div className="text-xs text-muted-foreground bg-muted/50 rounded-lg p-3 flex items-start gap-2">
              <Info className="size-4" />
              <span>{t("agents.token_impersonate_hint")}</span>
            </div>
          </div>        </Card>
      </div>

      <Card className="overflow-hidden">
        <div className="px-5 py-3 border-b border-border flex items-center justify-between">
          <h2 className="text-sm font-semibold text-foreground">
            <List className="size-4" />{t("nav.beacon_token")} ({tokenError ? "?" : tokens.length})
          </h2>          <div className="flex items-center gap-2">
            <div className="text-xs text-muted-foreground">
              <span className="inline-flex items-center gap-1.5">
                <span className="size-2 bg-warning rounded-full animate-pulse"></span>                {tokenError ? "?" : tokens.filter(tok => tok.active).length} active
              </span>
            </div>
          </div>
        </div>
        {tokenError && (
          <div role="alert" className="flex flex-wrap items-center gap-3 border-b border-border bg-warning/10 px-5 py-2.5 text-xs text-warning-ink">
            <AlertTriangle className="size-4 shrink-0" aria-hidden="true" />
            <span className="min-w-0 flex-1">
              {tokens.length > 0 ? t("agents.token_load_stale") : t("agents.token_load_failed")}
            </span>
            <Button onClick={() => { void loadTokens(); }} size="xs" variant="outline">{t("common.try_again")}</Button>
          </div>
        )}
        <div className="overflow-x-auto">
          <Table className="w-full text-sm">
            <TableHeader>
              <TableRow className="text-xs text-muted-foreground">
                <TableHead className="text-left px-5 py-3 font-normal">{t("agents.token_username")}</TableHead>
                <TableHead className="text-left px-4 py-3 font-normal">{t("agents.token_integrity")}</TableHead>
                <TableHead className="text-left px-4 py-3 font-normal">{t("agents.token_type")}</TableHead>
                <TableHead className="text-left px-4 py-3 font-normal">{t("agents.token_source")}</TableHead>
                <TableHead className="text-left px-4 py-3 font-normal">{t("agents.token_process")}</TableHead>
                <TableHead className="text-left px-4 py-3 font-normal">{t("agents.token_status")}</TableHead>
                <TableHead className="text-left px-4 py-3 font-normal">{t("agents.token_notes")}</TableHead>
                <TableHead className="text-left px-4 py-3 font-normal">{t("agents.token_created")}</TableHead>
                <TableHead className="text-left px-4 py-3 font-normal">{t("agents.token_actions")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody className="divide-y divide-border">
              {!loading && tokens.map((tok) => {
                const tid = tok.id ? String(tok.id) : "";
                const domain = tok.domain || "";
                const username = tok.username || "";
                const integrity = tok.integrity || "Medium";
                const source = tok.source || "steal";
                const tokenType = tok.token_type;
                const protocol = tok.protocol;
                const pid = tok.pid;
                const procName = tok.process_name;
                const active = tok.Active !== undefined ? tok.Active : tok.active;
                const createdAt = tok.created_at || "";
                const noteText = tok.note || tokenNotes[tid] || "";
                const isEditingNote = noteTargetId === tid;
                return (
                  <TableRow key={tid} className={`hover:bg-muted/50 transition-colors ${active ? "bg-warning/10" : ""}`}>
                    <TableCell className="px-5 py-3">
                      <span className="font-semibold text-foreground text-sm">{domain ? `${domain}\\${username}` : username || t("agents.unknown")}</span>
                    </TableCell>
                    <TableCell className="px-4 py-3">{getIntegrityBadge(integrity)}</TableCell>
                    <TableCell className="px-4 py-3">{getTokenTypeBadge(source, tokenType, protocol)}</TableCell>
                    <TableCell className="px-4 py-3">
                      <Badge variant={source === "steal" ? "destructive" : source === "make" || source === "create" ? "default" : "secondary"}>
                        {source}
                      </Badge>
                    </TableCell>
                    <TableCell className="px-4 py-3 text-xs font-mono text-muted-foreground">{pid ? `[${pid}]` : ""} {procName || ""}</TableCell>
                    <TableCell className="px-4 py-3">
                      {active ? (
                        <Badge variant="secondary" className="text-xs gap-1.5"><span className="size-2 bg-warning rounded-full animate-pulse"></span>{t("agents.token_active")}</Badge>
                      ) : (
                        <Badge variant="secondary" className="text-xs gap-1.5"><span className="size-2 bg-muted rounded-full"></span>{t("agents.token_inactive")}</Badge>
                      )}
                    </TableCell>
                    <TableCell className="px-4 py-3">
                      {isEditingNote ? (
                        <div className="flex items-center gap-1">
                          <Input
                            type="text"
                            value={tokenNotes[tid] || ""}
                            onChange={(e) => setTokenNotes(prev => ({ ...prev, [tid]: e.target.value }))}
                            className="w-24 h-7 text-xs"
                            placeholder={t("agents.token_note_ph")}
                            autoFocus
                          />
                          <Button variant="ghost" size="sm" onClick={() => handleNote(tid)} className="size-7 p-0 text-success-ink hover:text-success-ink">
                            <Check className="size-4" />
                          </Button>
                          <Button variant="ghost" size="sm" onClick={() => setNoteTargetId(null)} className="size-7 p-0 text-muted-foreground hover:text-foreground">
                            <X className="size-4" />
                          </Button>
                        </div>
                      ) : (
                        <Tooltip>
                          <TooltipTrigger render={<Button
                              variant="ghost"
                              size="sm"
                              onClick={() => { setNoteTargetId(tid); setTokenNotes(prev => ({ ...prev, [tid]: noteText })); }}
                              className="text-xs text-muted-foreground hover:text-primary"
                            />}>
                              <Pencil className="size-4" />
                              {noteText ? <span className="truncate max-w-20">{noteText}</span> : <span className="text-muted-foreground">{t("agents.token_add_note")}</span>}
                          </TooltipTrigger>
                          <TooltipContent>{t("agents.token_edit_note")}</TooltipContent>
                        </Tooltip>
                      )}
                    </TableCell>
                    <TableCell className="px-4 py-3 text-xs font-mono text-muted-foreground">{createdAt ? formatTime(createdAt) : ""}</TableCell>
                    <TableCell className="px-4 py-3">
                      <div className="flex items-center gap-1">
                        <Tooltip>
                          <TooltipTrigger render={<Button variant="ghost" size="sm" onClick={() => handleImpersonate(tid)} disabled={!active || activeAction === `impersonate-${tid}`} className={`size-8 p-0 ${active ? "" : "cursor-not-allowed"}`} />}>
                            {activeAction === `impersonate-${tid}` ? <Spinner size="sm" /> : <User className="size-4" />}
                          </TooltipTrigger>
                          <TooltipContent>{t("agents.token_impersonate")}</TooltipContent>
                        </Tooltip>
                        <Tooltip>
                          <TooltipTrigger render={<Button variant="ghost" size="sm" onClick={() => handleDrop(tid)} disabled={activeAction === `drop-${tid}`} className={`size-8 p-0 text-destructive-ink hover:text-destructive-ink/80 hover:bg-destructive/5`} />}>
                            {activeAction === `drop-${tid}` ? <Spinner size="xs" /> : <Trash2 className="size-3" />}
                          </TooltipTrigger>
                          <TooltipContent>{t("agents.token_drop")}</TooltipContent>
                        </Tooltip>
                      </div>
                    </TableCell>
                  </TableRow>
                );
              })}
              {loading && Array.from({ length: 3 }).map((_, i) => (<TableRow key={i}><TableCell colSpan={9} className="py-3 px-4"><Skeleton className="h-8 w-full" /></TableCell></TableRow>))}
              {!loading && !tokenError && tokens.length === 0 && (<TableRow><TableCell colSpan={9}><EmptyState compact icon={BadgeInfo} title={t("agents.token_no_tokens")} /></TableCell></TableRow>)}
            </TableBody>
          </Table>
        </div>      </Card>
      {modal}
    </PageContainer>
  );
}