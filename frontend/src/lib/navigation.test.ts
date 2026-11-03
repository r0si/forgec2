import { describe, expect, it } from "vitest";
import { NAV_ITEMS, NAV_SECTIONS, NAV_BY_HREF, NAV_SEGMENT_LABELS, sidebarNavSections } from "@/lib/navigation";

describe("navigation single source", () => {
  it("nav items have unique hrefs and label keys", () => {
    const hrefs = NAV_ITEMS.map((i) => i.href);
    expect(new Set(hrefs).size).toBe(hrefs.length);
    const labels = NAV_ITEMS.map((i) => i.labelKey);
    expect(new Set(labels).size).toBe(labels.length);
    expect(NAV_ITEMS.length).toBeGreaterThan(30);
  });

  it("every item label key uses the nav.* namespace", () => {
    for (const item of NAV_ITEMS) {
      expect(item.labelKey).toMatch(/^nav\./);
    }
  });

  it("NAV_BY_HREF maps every top-level page", () => {
    for (const item of NAV_ITEMS) {
      expect(NAV_BY_HREF[item.href]).toBe(item.labelKey);
    }
  });

  it("NAV_SEGMENT_LABELS covers every href normalized for breadcrumbs", () => {
    for (const item of NAV_ITEMS) {
      const seg = item.href.replace(/^\//, "").replace(/-/g, "_");
      expect(NAV_SEGMENT_LABELS[seg]).toBe(item.labelKey);
    }
  });

  it("sections are non-empty and follow the operator workstream order", () => {
    expect(NAV_SECTIONS.map((s) => s.titleKey)).toEqual([
      "operations",
      "logs",
      "build-deploy",
      "toolbox",
      "intelligence",
      "reporting",
      "administration",
      "lab",
    ]);
    for (const section of NAV_SECTIONS) {
      expect(section.items.length).toBeGreaterThan(0);
    }
  });

  it("surfaces every routed page in the sidebar, including lab and admin tooling", () => {
    const hrefs = sidebarNavSections().flatMap((s) => s.items.map((i) => i.href));
    for (const item of NAV_ITEMS) {
      expect(hrefs).toContain(item.href);
    }
    // Previously command-palette-only surfaces are now first-class nav entries.
    for (const href of ["/phishing", "/ntlm", "/users", "/tags", "/traffic", "/bloodhound"]) {
      expect(hrefs).toContain(href);
      expect(NAV_BY_HREF[href]).toBeTruthy();
    }
  });

  it("keeps every page discoverable in the command palette", () => {
    const paletteItems = NAV_ITEMS.filter((item) => item.discoverable !== false).map((item) => item.href);
    expect(paletteItems).toEqual(NAV_ITEMS.map((item) => item.href));
  });

  it("does not advertise merged routes as sidebar destinations", () => {
      const hrefs = NAV_ITEMS.map((i) => i.href);
      // All of these are tabs (of /generate, /events or /beacons/:id) rather
      // than destinations. /notifications joined them when the alerts tab
      // absorbed the standalone page; its unread badge rides on /timeline.
      for (const dead of [
        "/builds",
        "/profiles",
        "/packer",
        "/stager",
        "/tasks",
        "/files",
        "/notifications",
      ]) {
        expect(hrefs).not.toContain(dead);
      }
    });

  it("pins the four core operational destinations", () => {
    const primary = NAV_SECTIONS[0];
    expect(primary.pinned).toBe(true);
    expect(primary.items.length).toBeLessThanOrEqual(8);
      expect(primary.items.map((i) => i.href)).toEqual([
        "/dashboard",
        "/beacons",
        "/listeners",
      ]);
      const logs = NAV_SECTIONS.find((s) => s.titleKey === "logs");
      expect(logs?.items.map((i) => i.href)).toEqual(["/events", "/traffic", "/audit"]);
      // The notification count remains visible on the unified log entry.
      expect(logs?.items.some((i) => i.badge === "notifications")).toBe(true);
    });

  it("keeps administration destinations grouped instead of palette-only", () => {
    const admin = NAV_SECTIONS.find((s) => s.titleKey === "administration");
    const hrefs = (admin?.items ?? []).map((i) => i.href);
    expect(hrefs).toEqual(
      expect.arrayContaining(["/settings", "/users", "/roles", "/tags", "/groups", "/autotag"]),
    );
    expect(hrefs).not.toContain("/audit");
    expect(NAV_BY_HREF["/settings"]).toBe("nav.settings");
  });
});
