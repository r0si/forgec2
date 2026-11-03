import { describe, it, expect } from "vitest";
import { defaultSidebarSections, mergeSidebarSections } from "./sidebar-sections";

describe("mergeSidebarSections", () => {
  it("returns defaults when saved is null", () => {
    expect(mergeSidebarSections(null)).toEqual(defaultSidebarSections);
  });

  it("preserves known boolean prefs", () => {
    const merged = mergeSidebarSections({ lab: true, operations: false });
    expect(merged.lab).toBe(true);
    expect(merged.operations).toBe(false);
    expect(merged["intelligence"]).toBe(true);
  });

  it("ignores unknown keys from legacy storage", () => {
    const merged = mergeSidebarSections({
      ...defaultSidebarSections,
      obsolete: true,
    } as Record<string, boolean>);
    expect(merged).toEqual(defaultSidebarSections);
    expect("obsolete" in merged).toBe(false);
  });

  it("fills new default keys missing from old saved object", () => {
    const old = { operations: false, "build-deploy": true };
    const merged = mergeSidebarSections(old);
    expect(merged.operations).toBe(false);
    expect(merged.logs).toBe(true);
    expect(merged.lab).toBe(false);
    expect(merged.administration).toBe(false);
    expect(merged["build-deploy"]).toBe(true);
  });
});
