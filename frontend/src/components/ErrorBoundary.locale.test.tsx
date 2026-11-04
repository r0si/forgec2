import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import ErrorBoundary from "./ErrorBoundary";
import { I18nProvider, useI18n } from "@/lib/i18n";

vi.mock("@/lib/logger", () => ({ logger: { error: vi.fn() } }));
vi.mock("@/lib/i18n/zh", () => ({ zh: {
  "common.save": "保存",
  "error.boundary_title": "发生错误",
} }));

function Controls() {
  const { setLocale } = useI18n();
  return <>
    <button onClick={() => setLocale("zh")}>Chinese</button>
    <button onClick={() => setLocale("en")}>English</button>
  </>;
}
function Label() {
  const { t } = useI18n();
  return <output>{t("common.save")}</output>;
}
function Broken(): never { throw new Error("intentional test crash"); }

beforeEach(() => {
  localStorage.clear();
  vi.spyOn(console, "error").mockImplementation(() => {});
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

it("does not isolate healthy content from the parent language controls", async () => {
  render(<I18nProvider><Controls /><ErrorBoundary><Label /></ErrorBoundary></I18nProvider>);
  fireEvent.click(screen.getByText("Chinese"));
  await waitFor(() => expect(screen.getByRole("status").textContent).toBe("保存"));
  fireEvent.click(screen.getByText("English"));
  expect(screen.getByRole("status").textContent).toBe("Save");
});

it("keeps the error fallback subscribed to the parent language", async () => {
  render(<I18nProvider><Controls /><ErrorBoundary><Broken /></ErrorBoundary></I18nProvider>);
  fireEvent.click(screen.getByText("Chinese"));
  await screen.findByRole("heading", { name: "发生错误" });
  fireEvent.click(screen.getByText("English"));
  await screen.findByRole("heading", { name: "Something went wrong" });
});

it("can still translate a root failure without a parent provider", async () => {
  localStorage.setItem("forgec2_lang", "zh");
  render(<ErrorBoundary><Broken /></ErrorBoundary>);
  await screen.findByRole("heading", { name: "发生错误" });
});
