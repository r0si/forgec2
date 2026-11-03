export const defaultSidebarSections: Record<string, boolean> = {
  operations: true,
  logs: true,
  "build-deploy": true,
  toolbox: false,
  intelligence: true,
  reporting: false,
  administration: false,
  lab: false,
};

/** Bump when default open/closed sections change so operators pick up new defaults. */
export const SIDEBAR_SECTIONS_KEY = "forgec2_sidebar_sections_v5";
export const SIDEBAR_SECTIONS_LEGACY_KEY = "forgec2_sidebar_sections";

/** Merge saved prefs with current defaults so new section keys inherit defaults. */
export function mergeSidebarSections(
  saved: Record<string, boolean> | null | undefined,
  defaults: Record<string, boolean> = defaultSidebarSections,
): Record<string, boolean> {
  if (!saved || typeof saved !== "object" || Array.isArray(saved)) {
    return { ...defaults };
  }
  const next = { ...defaults };
  for (const key of Object.keys(defaults)) {
    if (typeof saved[key] === "boolean") next[key] = saved[key];
  }
  return next;
}
