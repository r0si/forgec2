import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import ErrorBoundary from "./ErrorBoundary";
import { logger } from "@/lib/logger";

vi.mock("@/lib/i18n", () => ({
  I18nFallbackProvider: ({ children }: { children: ReactNode }) => children,
  useI18n: () => ({ t: (key: string) => key }),
}));
vi.mock("@/lib/logger", () => ({ logger: { error: vi.fn() } }));

const sensitiveMessage = "C:/private/config.yaml: token=private-value";
function Child({ crash = true }: { crash?: boolean }) {
  if (crash) throw new Error(sensitiveMessage);
  return <p>Recovered content</p>;
}

beforeEach(() => {
  vi.spyOn(console, "error").mockImplementation(() => {});
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.clearAllMocks();
});

describe("ErrorBoundary", () => {
  it("announces a localized failure without rendering raw exception data", () => {
    render(<ErrorBoundary><Child /></ErrorBoundary>);
    expect(screen.getByRole("alert").textContent).toContain("error.boundary_message");
    expect(screen.queryByText(sensitiveMessage)).toBeNull();
    expect(logger.error).toHaveBeenCalled();
  });

  it("recovers on retry after the child stops throwing", () => {
    const view = render(<ErrorBoundary><Child /></ErrorBoundary>);
    view.rerender(<ErrorBoundary><Child crash={false} /></ErrorBoundary>);
    fireEvent.click(screen.getByRole("button", { name: "error.try_again" }));
    expect(screen.getByText("Recovered content")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("resets a latched failure when the route key changes", () => {
    const view = render(<ErrorBoundary resetKey="one"><Child /></ErrorBoundary>);
    view.rerender(<ErrorBoundary resetKey="two"><Child crash={false} /></ErrorBoundary>);
    expect(screen.getByText("Recovered content")).toBeTruthy();
  });

  it("honors an explicitly empty fallback", () => {
    const view = render(<ErrorBoundary fallback={null}><Child /></ErrorBoundary>);
    expect(view.container.textContent).toBe("");
  });

  it("preserves a caller-supplied fallback", () => {
    render(<ErrorBoundary fallback={<p>Custom fallback</p>}><Child /></ErrorBoundary>);
    expect(screen.getByText("Custom fallback")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
