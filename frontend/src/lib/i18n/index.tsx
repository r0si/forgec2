import { createContext, useContext, useState, useCallback, useEffect, useRef, useMemo, type ReactNode } from "react";
import { enBootstrap } from "./bootstrap";
import { en as enSync } from "@i18n-sync";

export type Locale = "en" | "zh";

// ── Lazy English dictionary fragments ────────────────────────────────────────
// The English dictionary is one dynamic chunk (~165 kB) fetched at startup. Two
// large namespaces are only needed by page chunks (`agents.*` by the beacon
// pages, `settings.*` by settings/roles), so they live in plain data modules
// that the route entry registers here. `t()` consults this table after the
// active dictionary, so a page translates from the moment it renders and the
// startup chunk stays smaller.
//
// The registry lives in THIS module on purpose: the provider already imports it,
// so the state is a single copy in the entry chunk. A separate registry module
// was shared only by the provider and the two fragment chunks, and Rolldown
// merged it into `i18n-agents` — which put the whole 53 kB agents dictionary
// back into first paint. Page chunks import this module anyway (for useI18n),
// so registration adds no bytes to them.
//
// zh is not split: it loads only for zh users and stays whole, so a zh user
// never depends on fragment registration order. An English fragment is still
// the fallback for a key missing from zh.
type Dict = Record<string, string>;

const fragments: Dict = {};

/** Merge a fragment into the lookup table. Idempotent per key. */
export function registerFragment(dict: Dict): void {
  for (const key in dict) {
    fragments[key] = dict[key];
  }
}

/** Value for a key registered by a loaded fragment, if any. */
export function fragmentValue(key: string): string | undefined {
  return fragments[key];
}

interface I18nContextType {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  t: (key: string, params?: Record<string, string | number>) => string;
  dir: "ltr" | "rtl";
  /** True once the full English dictionary has been loaded (or was sync). */
  ready: boolean;
}

const defaultI18n: I18nContextType = {
  locale: "en",
  setLocale: () => {},
  t: (key: string) => key,
  dir: "ltr",
  ready: true,
};
const I18nContext = createContext<I18nContextType>(defaultI18n);

// The full English dictionary used to sit on the first-paint path at ~244 kB.
// Only the shell is synchronous now (bootstrap.ts); the rest arrives as its own
// chunk and is merged in. Dev and test resolve @i18n-sync to the real
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

export function I18nProvider({ children, full = true }: { children: ReactNode; full?: boolean }) {
  const [locale, setLocaleState] = useState<Locale>("en");
  const localeRef = useRef<Locale>("en");
  const pendingRef = useRef<Locale | null>(null);
  const [ready, setReady] = useState<boolean>(() => enFull !== null || !full);

  // Production: the full dictionary is not on the first-paint path, so fetch it
  // once and re-render everything with it. A failure leaves the bootstrap slice
  // in place rather than blanking the UI.
  //
  // `full={false}` is for routes that only need the synchronous slice (the
  // login form): loading ~238 kB of translations before the operator has even
  // signed in was the largest avoidable transfer on the public demo's front
  // door. Authenticated routes mount a full provider instead.
  useEffect(() => {
    if (!full || enFull) return;
    let cancelled = false;
    loadEn().then(() => {
      if (!cancelled) setReady(true);
    }).catch(() => {
      if (!cancelled) setReady(true);
    });
    return () => {
      cancelled = true;
    };
  }, [full]);

  useEffect(() => {
    let saved: string | null = null;
    try {
      saved = localStorage.getItem("forgec2_lang");
    } catch {
      // Browser privacy policies can disable storage without disabling the UI.
    }
    if (saved === "zh") {
      pendingRef.current = "zh";
      loadZh().then(() => {
        if (pendingRef.current !== "zh") return;
        localeRef.current = "zh";
        activeLocale = "zh";
        setLocaleState("zh");
      }).catch(() => {
        // Keep the current dictionary; loadZh clears its cache for a retry.
      });
    } else if (saved === "en") {
      localeRef.current = "en";
      activeLocale = "en";
      setLocaleState("en");
    }
    return () => { pendingRef.current = null; };
  }, []);

  useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  const setLocale = useCallback((newLocale: Locale) => {
    // A same-language selection must also cancel an opposite pending load.
    pendingRef.current = newLocale;
    try {
      localStorage.setItem("forgec2_lang", newLocale);
    } catch {
      // Persisting a preference is optional; switching in memory still works.
    }
    try {
      document.cookie = "forgec2_lang=" + newLocale + "; path=/; max-age=31536000; SameSite=Strict";
    } catch {
      // Cookie access can be restricted independently of localStorage.
    }
    if (newLocale === localeRef.current) return;
    if (newLocale === "zh") {
      loadZh().then(() => {
        if (pendingRef.current !== "zh") return;
        localeRef.current = "zh";
        activeLocale = "zh";
        setLocaleState("zh");
      }).catch(() => {
        // Do not surface an unhandled rejection when the locale chunk is offline.
      });
    } else {
      localeRef.current = "en";
      activeLocale = "en";
      setLocaleState("en");
    }
    document.documentElement.dir = "ltr";
  }, []);

  // G2 fix: include locale in deps so `t` identity changes on language switch,
  // causing memoized children (Sidebar, CommandPalette items, document.title
  // effect) to re-render correctly. `ready` is a dep too: when the full
  // dictionary lands, t must be a new function so memoized children re-render
  // with the complete strings instead of the bootstrap slice.
  const t = useCallback((key: string, params?: Record<string, string | number>): string => {
    const dict = dictFor(localeRef.current);
    // Registered page fragments sit between the active dictionary and the
    // synchronous shell slice: a page that imported its fragment translates
    // immediately, and it is also the fallback when zh lacks a key.
    let val = dict[key] || fragmentValue(key) || bootstrapDict[key] || key;
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

/** Root error views need a provider, but nested boundaries must preserve the
 * application's live locale rather than shadowing it with independent state. */
export function I18nFallbackProvider({ children }: { children: ReactNode }) {
  const inherited = useContext(I18nContext);
  return inherited === defaultI18n ? <I18nProvider>{children}</I18nProvider> : children;
}
