
import { registerFragment } from "@/lib/i18n";
import { enBOF } from "@/lib/i18n/fragments/en.bof";
registerFragment(enBOF);

import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { PageContainer } from "@/components/ui/page-container";
import { Spinner } from "@/components/ui/spinner";
import { useBOFData } from "./components/useBOFData";
import { quickBOFLibrary } from "./components/types";
import type { BOFFile, QuickBOF } from "./components/types";
import { lazy, Suspense } from "react";

import { Card } from "@/components/ui/card";
import { StatCard } from "@/components/ui/animated-stat-card";
import { DataError } from "@/components/ui/data-state";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { BookOpen, Box, Check, PieChart, Terminal, Zap } from "lucide-react";

const BOFListTab = lazy(() => import("./components/BOFListTab"));
const BOFRepoTab = lazy(() => import("./components/BOFRepoTab"));
const BOFExecutionsTab = lazy(() => import("./components/BOFExecutionsTab"));

function bofFileId(file: BOFFile): string {
  return String(file.id ?? file.ID ?? "");
}

function bofFileName(file: BOFFile): string {
  return file.name || file.Name || "";
}

export default function BOFPage() {
  const { t } = useI18n();
  const {
    files,
    repoItems,
    executions,
    agents,
    loading,
    repoLoading,
    loadError,
    repoError,
    activeTab,
    setActiveTab,
    uploadBOF,
    deleteBOF,
    runBOF,
    editBOF,
    importFromUrl,
    loadFiles,
    loadRepo,
  } = useBOFData();

  const { confirm, modal } = useConfirm();

  const handleQuickRun = async (bof: QuickBOF) => {
    const bofFile = files.find((f) => bofFileName(f).toLowerCase() === bof.name.toLowerCase());
    if (!bofFile) {
      toast.error(t("bof.toast.not_uploaded", { name: bof.name }));
      return;
    }
    const target = agents[0];
    if (!target) {
      toast.error(t("bof.toast.no_agents"));
      return;
    }
    const ok = await confirm({
      title: t("bof.quick_run"),
      message: t("bof.quick_run_confirm", { name: bof.name, agent: target.hostname || target.id }),
      confirmText: t("bof.run"),
      danger: true,
    });
    if (!ok) return;
    runBOF(bofFileId(bofFile), target.id, bof.args);
  };

  const handleDelete = async (id: string | number) => {
    const key = String(id);
    const file = files.find((f) => bofFileId(f) === key);
    const ok = await confirm({
      title: t("bof.delete_title"),
      message: t("bof.delete_message", { name: (file && bofFileName(file)) || t("bof.unnamed") }),
      danger: true,
    });
    if (!ok) return;
    deleteBOF(key);
  };

  if (loading)
    return (
      <PageContainer title={t("bof.title")} subtitle={t("bof.subtitle")}>
        <div className="flex items-center justify-center py-16">
          <Spinner />
        </div>
      </PageContainer>
    );

  return (
    <PageContainer title={t("bof.title")} subtitle={t("bof.subtitle")}>

      {files.length === 0 && loadError ? (
        // First-load failure: the stat cards would all read 0 and the quick
        // tab's badges would read "not installed" - a reassuring default for
        // a request failure, so replace the whole view instead.
        <DataError message={t("bof.data_unreadable", { message: loadError })} onRetry={() => void loadFiles()} />
      ) : (
        <>
      <div className="grid grid-cols-2 sm:grid-cols-4 gap-4 sm:gap-5 mb-6">
        <StatCard label={t("bof.stat_uploaded")} value={files.length} color="primary" icon={<Box className="size-4" />} iconSide="left" />
        <StatCard label={t("bof.stat_executions")} value={executions.length} color="success" icon={<Check className="size-4" />} iconSide="left" />
        <StatCard
          label={t("bof.stat_success_rate")}
          value={executions.length > 0 ? `${Math.round((executions.filter((e) => (e.status) === "success").length / executions.length) * 100)}%` : "N/A"}
          color="warning"
          icon={<PieChart className="size-4" />}
          iconSide="left"
        />
        <StatCard label={t("bof.stat_available_agents")} value={agents.length} color="primary" icon={<BookOpen className="size-4" />} iconSide="left" />
      </div>

      <Tabs value={activeTab} onValueChange={(v) => setActiveTab(v as typeof activeTab)}>
        <TabsList sticky>
          {[
            { key: "bof", Icon: Box, label: t("bof.tab_files") },
            { key: "exec", Icon: Terminal, label: t("bof.tab_exec") },
            { key: "quick", Icon: Zap, label: t("bof.tab_quick") },
            { key: "repo", Icon: BookOpen, label: t("bof.tab_repo") },
          ].map((tab) => (
            <TabsTrigger key={tab.key} value={tab.key} className="gap-1.5">
              <tab.Icon className="size-3" />
              {tab.label}
            </TabsTrigger>
          ))}
        </TabsList>

      <TabsContent value="bof">
        <Suspense fallback={null}>
          <BOFListTab
            files={files}
            loading={loading}
            onUpload={(file, arch, name, desc) => uploadBOF(file, arch, name, desc)}
            onDelete={handleDelete}
            onRun={(id, agentId, args) => runBOF(String(id), agentId, args)}
            onEdit={editBOF}
            agents={agents}
          />
        </Suspense>
      </TabsContent>

      <TabsContent value="exec"><Suspense fallback={null}><BOFExecutionsTab executions={executions} loading={loading} error={loadError} onRetry={() => void loadFiles()} /></Suspense></TabsContent>

      <TabsContent value="quick">
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
          {quickBOFLibrary.map((bof) => {
            const isUploaded = files.some((f) => bofFileName(f).toLowerCase() === bof.name.toLowerCase());
            return (
              <Card key={bof.name} className="p-(--card-spacing) hover:shadow-md transition-shadow">
                <div className="flex items-center justify-between mb-2">
                  <div className="text-sm font-medium text-foreground font-mono">{bof.name}</div>
                  <Badge variant={isUploaded ? "default" : "secondary"} className="text-micro-sm">
                    {isUploaded ? t("bof.uploaded_ready") : t("bof.not_installed")}
                  </Badge>
                </div>
                <div className="text-xs text-muted-foreground mb-1">{t(bof.descKey)}</div>
                <div className="flex items-center justify-between mt-3">
                  <Badge variant="secondary" className="text-micro-sm font-mono">{bof.arch}</Badge>
                  <Button size="sm" onClick={() => handleQuickRun(bof)} disabled={!isUploaded}>
                    <Zap className="size-4" />{t("bof.quick_run")}
                  </Button>
                </div>
              </Card>
            );
          })}
        </div>
      </TabsContent>

      <TabsContent value="repo">
        <Suspense fallback={null}>
          <BOFRepoTab repoItems={repoItems} loading={repoLoading} error={repoError} onRetry={() => void loadRepo()} onImportUrl={importFromUrl} />
        </Suspense>
      </TabsContent>
      </Tabs>
        </>
      )}

      {modal}
    </PageContainer>
  );
}
