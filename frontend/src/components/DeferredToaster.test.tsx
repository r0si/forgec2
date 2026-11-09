import { render, screen, act } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("sonner", () => ({
  Toaster: ({ theme, richColors }: { theme?: string; richColors?: boolean }) => (
    <div data-testid="toaster" data-theme={theme} data-rich={String(richColors)} />
  ),
}));
vi.mock("@/lib/theme", () => ({ useTheme: () => ({ resolved: "dark" }) }));

import DeferredToaster from "./DeferredToaster";

describe("DeferredToaster", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("mounts the toaster only after the idle window", async () => {
    vi.useFakeTimers();
    render(<DeferredToaster />);
    // The 33 kB sonner chunk must not be part of the first render.
    expect(screen.queryByTestId("toaster")).toBeNull();

    await act(async () => {
      await vi.runAllTimersAsync();
    });
    expect(screen.getByTestId("toaster").getAttribute("data-theme")).toBe("dark");
    expect(screen.getByTestId("toaster").getAttribute("data-rich")).toBe("true");
  });
});
