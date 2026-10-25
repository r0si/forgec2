import { describe, it, expect, vi, beforeEach } from "vitest";
import { useEffect } from "react";
import { render, screen, waitFor, act } from "@testing-library/react";

// The zh dictionary is a dynamic import in the provider; stub it so the swap
// is observable without loading the real ~200 KB module. `en` stays real, so
// the fallback path is exercised against the shipped dictionary.
vi.mock("./zh", () => ({
  zh: {
    "common.save": "保存",
    "common.type_to_confirm": '输入 "{text}" 以确认',
  },
}));

import { I18nProvider, useI18n, getLocale } from "./index";

type Locale = "en" | "zh";
type Snap = {
  t?: (key: string, params?: Record<string, string | number>) => string;
  setLocale?: (locale: Locale) => void;
};

// Captured in an effect, not during render: RTL's act() flushes effects, and
// react-hooks/immutability forbids writing into a prop object in render.
const captured: Snap = {};

// A missing key must not appear as a literal t("...") call: check-i18n.mjs
// scans test files too and would flag it as a used-but-undefined key.
const missingKey = "no.such.key";

function Probe() {
  const { t, setLocale } = useI18n();
  useEffect(() => {
    captured.t = t;
    captured.setLocale = setLocale;
  });
  return (
    <output data-testid="probe">
      {getLocale()}|{t("common.save")}|{t("common.type_to_confirm", { text: "DELETE" })}|{t(missingKey)}
    </output>
  );
}

describe("I18nProvider", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.lang = "en";
    document.cookie = "forgec2_lang=; path=/; max-age=0";
  });

  it("defaults to en and falls back to the key for a missing entry", () => {
    render(
      <I18nProvider>
        <Probe />
      </I18nProvider>,
    );

    expect(screen.getByTestId("probe").textContent).toBe(
      "en|Save|Type DELETE to confirm|no.such.key",
    );
    expect(document.documentElement.lang).toBe("en");
    expect(getLocale()).toBe("en");
  });

  it("boots zh from localStorage, loads the dictionary and swaps the document language", async () => {
    localStorage.setItem("forgec2_lang", "zh");

    render(
      <I18nProvider>
        <Probe />
      </I18nProvider>,
    );

    await waitFor(() => {
      expect(screen.getByTestId("probe").textContent).toBe(
        'zh|保存|输入 "DELETE" 以确认|no.such.key',
      );
    });
    // `lang` is applied by a separate effect keyed on locale, so it can land a
    // commit after the text does; asserting it synchronously flaked under load.
    await waitFor(() => expect(document.documentElement.lang).toBe("zh"));
    expect(getLocale()).toBe("zh");
  });

  it("setLocale swaps the live dictionary, persists the choice and rotates t", async () => {
    render(
      <I18nProvider>
        <Probe />
      </I18nProvider>,
    );
    const t0 = captured.t;

    // en -> zh: the dict resolves asynchronously (cached after the boot test
    // or fetched on demand), then the provider re-renders.
    await act(async () => {
      captured.setLocale!("zh");
    });
    await waitFor(() => {
      expect(screen.getByTestId("probe").textContent).toContain("保存");
    });
    expect(localStorage.getItem("forgec2_lang")).toBe("zh");
    expect(document.cookie).toContain("forgec2_lang=zh");
    expect(captured.t).not.toBe(t0);
    expect(getLocale()).toBe("zh");

    // zh -> en is synchronous and rotates the identity again (G2: memoized
    // children re-render on a language switch).
    act(() => {
      captured.setLocale!("en");
    });
    expect(screen.getByTestId("probe").textContent).toContain("Save");
    const t1 = captured.t;
    expect(t1).not.toBe(t0);
    expect(document.cookie).toContain("forgec2_lang=en");

    // Same-locale calls are a no-op: no re-render, stable t identity.
    act(() => {
      captured.setLocale!("en");
    });
    expect(captured.t).toBe(t1);
  });
});
