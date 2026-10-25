import { createContext, useContext, useState, useCallback, useEffect, useRef, useMemo, type ReactNode } from "react";
import { enBootstrap } from "./bootstrap";
import { en as enSync } from "@i18n-sync";

export type Locale = "en" | "zh";

interface I18nContextType {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  t: (key: string, params?: Record<string, string | number>) => string;
  dir: "ltr" | "rtl";
  /** True once the full English dictionary has been loaded (or was sync). */
  ready: boolean;
}

const I18nContext = createContext<I18nContextType>({
  locale: "en",
  setLocale: () => {},
  t: (key: string) => key,
  dir: "ltr",
  ready: true,
});

type Dict = Record<string, string>;

// The full English dictionary is ~239 kB and used to sit on the first-paint
// path. Only the shell is synchronous now (bootstrap.ts); the rest arrives as
// its own chunk and is merged in. Dev and test resolve @i18n-sync to the real
// dictionary so assertions see translated text immediately; a production build
// resolves it to an empty module.
const bootstrapDict: Dict = enBootstrap;
const zhRef: { current: Dict | null } = { current: null };
let zhPromise: Promise<Dict> | null = null;
let enFull: Dict | null = enSync ?? null;
let enPromise: Promise<Dict> | null = null;

// G4 fix: on chunk load failure, reset the cached rejected promise so the
// user can retry (rather than being stuck on the bootstrap slice).
function loadEn(): Promise<Dict> {
  if (enFull) return Promise.resolve(enFull);
  if (!enPromise) {
    enPromise = import("./en").then((m) => {
      enFull = m.en;
      return m.en;
    }).catch((err) => {
      enPromise = null;
      throw err;
    });
  }
  return enPromise;
}

function dictFor(locale: Locale): Dict {
  if (locale === "zh" && zhRef.current) return zhRef.current;
  return enFull ?? bootstrapDict;
}

// Mirror of the active locale for non-React formatters (`lib/intl.ts`),
// updated wherever the provider commits a language change below.
let activeLocale: Locale = "en";

/** UI language for code that formats text outside the React tree. */
export function getLocale(): Locale {
  return activeLocale;
}

// G4 fix: on chunk load failure, reset the cached rejected promise so the
// user can retry by switching language again (rather than being stuck).
function loadZh(): Promise<Dict> {
  if (zhRef.current) return Promise.resolve(zhRef.current);
  if (!zhPromise) {
    zhPromise = import("./zh").then((m) => {
      zhRef.current = m.zh;
      return m.zh;
    }).catch((err) => {
      zhPromise = null;
      throw err;
    });
  }
  return zhPromise;
}

export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>("en");
  const localeRef = useRef<Locale>("en");
  const pendingRef = useRef<Locale | null>(null);
  const [ready, setReady] = useState<boolean>(() => enFull !== null);

  // Production: the full dictionary is not on the first-paint path, so fetch it
  // once and re-render everything with it. A failure leaves the bootstrap slice
  // in place rather than blanking the UI.
  useEffect(() => {
    if (enFull) return;
    let cancelled = false;
    loadEn().then(() => {
      if (!cancelled) setReady(true);
    }).catch(() => {
      if (!cancelled) setReady(true);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    const saved = localStorage.getItem("forgec2_lang") as Locale;
    if (saved && saved !== "en") {
      pendingRef.current = "zh";
      loadZh().then(() => {
        if (pendingRef.current !== "zh") return;
        localeRef.current = "zh";
        activeLocale = "zh";
        setLocaleState("zh");
      });
    } else if (saved === "en") {
      localeRef.current = "en";
      activeLocale = "en";
      setLocaleState("en");
    }
  }, []);

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  const setLocale = useCallback((newLocale: Locale) => {
    if (newLocale === localeRef.current) return;
    pendingRef.current = newLocale;
    if (newLocale === "zh") {
      loadZh().then(() => {
        if (pendingRef.current !== "zh") return;
        localeRef.current = "zh";
        activeLocale = "zh";
        setLocaleState("zh");
      });
    } else {
      localeRef.current = "en";
      activeLocale = "en";
      setLocaleState("en");
    }
    localStorage.setItem("forgec2_lang", newLocale);
    document.cookie = "forgec2_lang=" + newLocale + "; path=/; max-age=31536000; SameSite=Strict";
    document.documentElement.lang = newLocale;
    document.documentElement.dir = "ltr";
  }, []);

  // G2 fix: include locale in deps so `t` identity changes on language switch,
  // causing memoized children (Sidebar, CommandPalette items, document.title
  // effect) to re-render correctly. `ready` is a dep too: when the full
  // dictionary lands, t must be a new function so memoized children re-render
  // with the complete strings instead of the bootstrap slice.
  const t = useCallback((key: string, params?: Record<string, string | number>): string => {
    const dict = dictFor(localeRef.current);
    let val = dict[key] || bootstrapDict[key] || key;
    if (params) {
      Object.entries(params).forEach(([k, v]) => {
        val = val.replace(new RegExp(`\\{${k}\\}`, "g"), String(v));
      });
    }
    return val;
  // eslint-disable-next-line react-hooks/exhaustive-deps -- locale/ready are read via refs and state; the deps are intentional (G2) so t rotates on language switch and on dictionary load.
  }, [locale, ready]);
  const dir: "ltr" | "rtl" = "ltr";
  const value = useMemo(() => ({ locale, setLocale, t, dir, ready }), [locale, setLocale, t, dir, ready]);

  return (
    <I18nContext.Provider value={value}>
      {children}
    </I18nContext.Provider>
  );
}

export function useI18n() {
  return useContext(I18nContext);
}