import { act, fireEvent, render, renderHook, screen } from "@testing-library/react";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { ThemeProvider, useTheme } from "./theme";
import { StrictMode, useEffect } from "react";

interface MediaMock {
  matches: boolean;
  listeners: Array<() => void>;
  removeEventListener: ReturnType<typeof vi.fn>;
}

let media: MediaMock = { matches: false, listeners: [], removeEventListener: vi.fn() };

/** jsdom has no matchMedia; the theme provider reads it on mount and on change. */
function installMatchMedia() {
  media = { matches: false, listeners: [], removeEventListener: vi.fn() };
  vi.stubGlobal(
    "matchMedia",
    vi.fn((query: string) => ({
      get matches() {
        return media.matches;
      },
      media: query,
      addEventListener: (_type: string, cb: () => void) => {
        media.listeners.push(cb);
      },
      removeEventListener: media.removeEventListener,
    })),
  );
}

function fireMediaChange() {
  for (const cb of media.listeners) cb();
}

function Probe() {
  const { theme, resolved, setTheme } = useTheme();
  return (
    <div>
      <output data-testid="probe">{`${theme}|${resolved}`}</output>
      <button data-testid="to-light" type="button" onClick={() => setTheme("light")}>
        light
      </button>
      <button data-testid="to-dark" type="button" onClick={() => setTheme("dark")}>
        dark
      </button>
      <button data-testid="to-system" type="button" onClick={() => setTheme("system")}>
        system
      </button>
    </div>
  );
}

function InitialChoice() {
  const { setTheme } = useTheme();
  useEffect(() => { setTheme("dark"); }, [setTheme]);
  return <Probe />;
}

async function mount(expectedAfterBoot: string) {
  render(
    <ThemeProvider>
      <Probe />
    </ThemeProvider>,
  );
  // Flush the boot microtask deterministically before interacting: the
  // initial render already shows "light|light", so polling would race it.
  await act(async () => {});
  expect(screen.getByTestId("probe").textContent).toBe(expectedAfterBoot);
}

describe("ThemeProvider", () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.classList.remove("dark");
    installMatchMedia();
  });
  afterEach(() => {
    document.documentElement.classList.remove("dark");
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("returns the light defaults outside a provider", () => {
    const { result } = renderHook(() => useTheme());
    expect(result.current.theme).toBe("light");
    expect(result.current.resolved).toBe("light");
    act(() => result.current.setTheme("dark")); // context default is a no-op
    expect(result.current.theme).toBe("light");
  });

  it("applies the stored theme on boot", async () => {
    localStorage.setItem("forgec2_theme", "dark");
    await mount("dark|dark");
    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });

  it("ignores an unknown stored theme and stays light", async () => {
    localStorage.setItem("forgec2_theme", "neon");
    await mount("light|light");
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });

  it("setTheme persists the choice and toggles the dark class", async () => {
    await mount("light|light");

    fireEvent.click(screen.getByTestId("to-dark"));
    expect(localStorage.getItem("forgec2_theme")).toBe("dark");
    expect(screen.getByTestId("probe").textContent).toBe("dark|dark");
    expect(document.documentElement.classList.contains("dark")).toBe(true);

    fireEvent.click(screen.getByTestId("to-light"));
    expect(localStorage.getItem("forgec2_theme")).toBe("light");
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });

  it("system follows prefers-color-scheme and reacts to media changes", async () => {
    await mount("light|light");
    fireEvent.click(screen.getByTestId("to-system"));
    expect(localStorage.getItem("forgec2_theme")).toBe("system");
    expect(document.documentElement.classList.contains("dark")).toBe(false);

    media.matches = true;
    act(() => fireMediaChange());
    expect(screen.getByTestId("probe").textContent).toBe("system|dark");
    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });

  it("media changes are ignored while a fixed theme is stored", async () => {
    await mount("light|light");
    fireEvent.click(screen.getByTestId("to-light"));

    media.matches = true;
    act(() => fireMediaChange());
    expect(document.documentElement.classList.contains("dark")).toBe(false);
    expect(screen.getByTestId("probe").textContent).toBe("light|light");
  });

  it("removes the media listener on unmount", async () => {
    const { unmount } = render(
      <ThemeProvider>
        <Probe />
      </ThemeProvider>,
    );
    await act(async () => {}); // let the boot microtask settle first

    unmount();
    expect(media.removeEventListener).toHaveBeenCalled();
  });

  it("still boots when browser storage is blocked", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new DOMException("Blocked", "SecurityError");
    });
    await mount("light|light");
  });

  it("keeps system mode working in memory when preference writes fail", async () => {
    await mount("light|light");
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("Full", "QuotaExceededError");
    });
    fireEvent.click(screen.getByTestId("to-system"));
    expect(screen.getByTestId("probe").textContent).toBe("system|light");
    media.matches = true;
    act(() => fireMediaChange());
    expect(screen.getByTestId("probe").textContent).toBe("system|dark");
  });

  it("does not overwrite a user choice with the queued boot preference", async () => {
    render(<ThemeProvider><Probe /></ThemeProvider>);
    fireEvent.click(screen.getByTestId("to-dark"));
    await act(async () => {});
    expect(screen.getByTestId("probe").textContent).toBe("dark|dark");
  });

  it("does not apply a queued boot theme after unmount", async () => {
    localStorage.setItem("forgec2_theme", "dark");
    const view = render(<ThemeProvider><Probe /></ThemeProvider>);
    view.unmount();
    await act(async () => {});
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });

  it("falls back safely when matchMedia is unavailable", async () => {
    vi.stubGlobal("matchMedia", undefined);
    localStorage.setItem("forgec2_theme", "system");
    await mount("system|light");
    fireEvent.click(screen.getByTestId("to-dark"));
    expect(screen.getByTestId("probe").textContent).toBe("dark|dark");
  });

  it("does not restore stale system mode after a failed fixed-theme save", async () => {
    localStorage.setItem("forgec2_theme", "system");
    await mount("system|light");
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("Full", "QuotaExceededError");
    });
    fireEvent.click(screen.getByTestId("to-dark"));
    act(() => fireMediaChange());
    expect(screen.getByTestId("probe").textContent).toBe("dark|dark");
    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });

  it("supports legacy media listeners and releases the exact callback", async () => {
    const addListener = vi.fn<(callback: () => void) => void>();
    const removeListener = vi.fn();
    const legacy = { matches: false, addListener, removeListener };
    vi.stubGlobal("matchMedia", () => legacy);
    const view = render(<ThemeProvider><Probe /></ThemeProvider>);
    await act(async () => {});
    fireEvent.click(screen.getByTestId("to-system"));
    const callback = addListener.mock.calls[0][0];
    legacy.matches = true;
    act(() => callback());
    expect(screen.getByTestId("probe").textContent).toBe("system|dark");
    view.unmount();
    expect(removeListener).toHaveBeenCalledWith(callback);
  });

  it("preserves a child's mount-time choice when storage cannot persist it", async () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("Blocked", "SecurityError");
    });
    render(<StrictMode><ThemeProvider><InitialChoice /></ThemeProvider></StrictMode>);
    await act(async () => {});
    expect(screen.getByTestId("probe").textContent).toBe("dark|dark");
    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });
});
