import { registerFragment } from "@/lib/i18n";
import { enAgents } from "@/lib/i18n/fragments/en.agents";
import { enSettings } from "@/lib/i18n/fragments/en.settings";
registerFragment(enAgents);
registerFragment(enSettings);
import { Link } from "react-router-dom";
import { ArrowRight, Layers, ListFilter, Tags, Wand2 } from "lucide-react";
import { PageContainer } from "@/components/ui/page-container";
import { Card } from "@/components/ui/card";
import { useI18n } from "@/lib/i18n";
import { useAppStore } from "@/lib/store";
import { canAny } from "@/lib/permissions";
import type { PermissionKey } from "@/lib/permission-keys";

type AssetTool = {
  href: string;
  titleKey: string;
  descriptionKey: string;
  icon: typeof Tags;
  tone: string;
  perms: PermissionKey[];
};

const TOOLS: AssetTool[] = [
  {
    href: "/tags",
    titleKey: "nav.tags",
    descriptionKey: "asset_org.tags_desc",
    icon: Tags,
    tone: "bg-primary/10 text-primary",
    perms: ["agents.write"],
  },
  {
    href: "/groups",
    titleKey: "nav.groups",
    descriptionKey: "asset_org.groups_desc",
    icon: Layers,
    tone: "bg-success/10 text-success-ink",
    perms: ["groups.read"],
  },
  {
    href: "/autotag",
    titleKey: "nav.autotag",
    descriptionKey: "asset_org.autotag_desc",
    icon: Wand2,
    tone: "bg-warning/10 text-warning-ink",
    perms: ["settings.read"],
  },
];

export default function AssetOrganizationPage() {
  const { t } = useI18n();
  const permissions = useAppStore((state) => state.currentPermissions);
  const visibleTools = TOOLS.filter((tool) => !permissions || canAny(permissions, tool.perms));
  const descriptions: Record<string, string> = {
    "/tags": t("asset_org.tags_desc"),
    "/groups": t("asset_org.groups_desc"),
    "/autotag": t("asset_org.autotag_desc"),
  };

  return (
    <PageContainer
      title={t("asset_org.title")}
      subtitle={t("asset_org.subtitle")}
      icon={<ListFilter className="size-5" />}
      variant="standard"
    >
      <div className="grid gap-4 md:grid-cols-3">
        {visibleTools.map((tool) => {
          const Icon = tool.icon;
          return (
            <Card key={tool.href} className="group flex min-h-44 flex-col justify-between border-border/70 p-(--card-spacing) transition-colors hover:border-primary/50 hover:bg-muted/20">
              <div>
                <div className={`mb-4 flex size-10 items-center justify-center rounded-xl ${tool.tone}`}>
                  <Icon className="size-5" aria-hidden="true" />
                </div>
                <h2 className="font-semibold text-foreground">{t(tool.titleKey)}</h2>
                <p className="mt-1 text-sm leading-6 text-muted-foreground">{descriptions[tool.href]}</p>
              </div>
              <Link to={tool.href} className="mt-5 inline-flex w-fit items-center rounded-md px-0 py-2 text-sm font-medium text-primary outline-none transition-colors hover:text-primary/80 focus-visible:ring-2 focus-visible:ring-ring">
                {t("asset_org.open")}
                <ArrowRight className="ml-1.5 size-4 transition-transform group-hover:translate-x-0.5" aria-hidden="true" />
              </Link>
            </Card>
          );
        })}
      </div>
    </PageContainer>
  );
}
