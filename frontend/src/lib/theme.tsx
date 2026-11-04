
import { createContext, useContext, useState, useCallback, useEffect, useRef, useMemo, ReactNode } from "react";
import { DEFAULT_THEME, resolveStoredTheme, type Theme } from "./theme-defaults";

export type { Theme };

interface ThemeContextType {
  theme: Theme;
  setTheme: (theme: Theme) => void;
  resolved: "light" | "dark";
}

const ThemeContext = createContext<ThemeContextType>({
  theme: DEFAULT_THEME,
  setTheme: () => {},
  resolved: "light",
});

function colorSchemeQuery(): MediaQueryList | null {
  try {
    return window.matchMedia?.("(prefers-color-scheme: dark)") ?? null;
  } catch {
    return null;
  }
}

function applyTheme(theme: Theme, systemDark: boolean) {
  const dark =
    theme === "dark" ||
    (theme === "system" && systemDark);
  document.documentElement.classList.toggle("dark", dark);
  return dark ? "dark" : "light";
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<Theme>(DEFAULT_THEME);
  const [resolved, setResolved] = useState<"light" | "dark">("light");
  const themeRef = useRef<Theme>(DEFAULT_THEME);
  const choiceVersion = useRef(0);

  useEffect(() => {
    let initial = DEFAULT_THEME;
    try {
      initial = resolveStoredTheme(localStorage.getItem("forgec2_theme"));
    } catch {
      // Storage restrictions must not prevent the application from mounting.
    }
    const mq = colorSchemeQuery();
    let cancelled = false;
    Promise.resolve().then(() => {
      // Children may choose a theme in their mount effect before this effect
      // starts; any explicit choice outranks the stored startup preference.
      if (cancelled || choiceVersion.current !== 0) return;
      themeRef.current = initial;
      setThemeState(initial);
      setResolved(applyTheme(initial, mq?.matches ?? false));
    });

    const onChange = () => {
      if (!cancelled && themeRef.current === "system") {
        setResolved(applyTheme("system", mq?.matches ?? false));
      }
    };
    if (mq?.addEventListener) mq.addEventListener("change", onChange);
    else mq?.addListener?.(onChange);
    return () => {
      cancelled = true;
      if (mq?.removeEventListener) mq.removeEventListener("change", onChange);
      else mq?.removeListener?.(onChange);
    };
  }, []);

  const setTheme = useCallback((next: Theme) => {
    choiceVersion.current += 1;
    themeRef.current = next;
    setThemeState(next);
    try {
      localStorage.setItem("forgec2_theme", next);
    } catch {
      // The in-memory preference remains authoritative when persistence fails.
    }
    setResolved(applyTheme(next, colorSchemeQuery()?.matches ?? false));
  }, []);

  const value = useMemo(() => ({ theme, setTheme, resolved }), [theme, setTheme, resolved]);

  return (
    <ThemeContext.Provider value={value}>
      {children}
    </ThemeContext.Provider>
  );
}

export function useTheme() {
  return useContext(ThemeContext);
}
