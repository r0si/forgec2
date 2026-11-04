import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, act } from "@testing-library/react";
import { ConfirmModal } from "@/components/ui/confirm-modal";

vi.mock("@/lib/i18n", () => ({
  getLocale: () => "en",
  useI18n: () => ({
    t: (key: string) => {
      const map: Record<string, string> = {
        "common.confirm": "Confirm",
        "common.cancel": "Cancel",
        "common.type_to_confirm": 'Type "{text}" to confirm',
      };
      return map[key] || key;
    },
  }),
}));

describe("ConfirmModal", () => {
  it("confirms immediately when no requireText is set", () => {
    const onConfirm = vi.fn();
    render(
      <ConfirmModal open title="Delete" message="Delete this?" confirmText="Delete" onConfirm={onConfirm} onCancel={() => {}} />,
    );
    const btn = screen.getByRole("button", { name: "Delete" });
    expect(btn.hasAttribute("disabled")).toBe(false);
    fireEvent.click(btn);
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("requires exact text before confirming", () => {
    const onConfirm = vi.fn();
    render(
      <ConfirmModal
        open
        title="Delete"
        message="Delete this?"
        confirmText="Delete"
        requireText="prod-box"
        onConfirm={onConfirm}
        onCancel={() => {}}
      />,
    );
    const btn = screen.getByRole("button", { name: "Delete" });
    expect(btn.hasAttribute("disabled")).toBe(true);

    const input = screen.getByRole("textbox");
    fireEvent.change(input, { target: { value: "prod-box" } });
    expect(btn.hasAttribute("disabled")).toBe(false);
    fireEvent.click(btn);
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("stays disabled on partial match", () => {
    render(
      <ConfirmModal
        open
        title="Delete"
        message="Delete this?"
        confirmText="Delete"
        requireText="prod-box"
        onConfirm={() => {}}
        onCancel={() => {}}
      />,
    );
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "prod" } });
    expect(screen.getByRole("button", { name: "Delete" }).hasAttribute("disabled")).toBe(true);
  });

  it("associates the dialog with its confirmation explanation", () => {
    render(<ConfirmModal open title="Confirm change" message="Review this change carefully." onConfirm={() => {}} onCancel={() => {}} />);
    const dialog = screen.getByRole("dialog", { name: "Confirm change" });
    const descriptions = (dialog.getAttribute("aria-describedby") ?? "").split(/\s+/).filter(Boolean);
    expect(descriptions.map(id => document.getElementById(id)?.textContent).join(" ")).toContain("Review this change carefully.");
  });

  it("uses unique input labels for independent dialog instances", () => {
    render(<>
      <ConfirmModal open title="First" message="First change" requireText="first" onConfirm={() => {}} onCancel={() => {}} />
      <ConfirmModal open title="Second" message="Second change" requireText="second" onConfirm={() => {}} onCancel={() => {}} />
    </>);
    const inputs = [...document.querySelectorAll<HTMLInputElement>("input")];
    expect(inputs).toHaveLength(2);
    expect(new Set(inputs.map(input => input.id)).size).toBe(2);
    for (const input of inputs) {
      const label = document.querySelector<HTMLLabelElement>(`label[for="${input.id}"]`);
      expect(label?.control).toBe(input);
    }
  });

  it("exposes pending confirmation state to assistive technology", async () => {
    let finish!: () => void;
    const pending = new Promise<void>(resolve => { finish = resolve; });
    render(<ConfirmModal open title="Confirm change" message="Review" onConfirm={() => pending} onCancel={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    expect(screen.getByRole("dialog").getAttribute("aria-busy")).toBe("true");
    await act(async () => { finish(); await pending; });
    expect(screen.getByRole("dialog").getAttribute("aria-busy")).toBe("false");
  });

  it("waits for promise-compatible results instead of using instanceof", async () => {
    let finish!: () => void;
    const pending = new Promise<void>(resolve => { finish = resolve; });
    const compatible: Promise<void> = {
      then: pending.then.bind(pending),
      catch: pending.catch.bind(pending),
      finally: pending.finally.bind(pending),
      [Symbol.toStringTag]: "Promise",
    };
    const onConfirm = vi.fn(() => compatible);
    render(<ConfirmModal open title="Confirm change" message="Review" onConfirm={onConfirm} onCancel={() => {}} />);
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    expect(screen.getByRole("dialog").getAttribute("aria-busy")).toBe("true");
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
    await act(async () => { finish(); await pending; });
    expect(screen.getByRole("dialog").getAttribute("aria-busy")).toBe("false");
  });

  it("does not unlock a reopened dialog when the previous request finishes", async () => {
    let finishFirst!: () => void;
    let finishSecond!: () => void;
    const first = new Promise<void>(resolve => { finishFirst = resolve; });
    const second = new Promise<void>(resolve => { finishSecond = resolve; });
    const onConfirm = vi.fn().mockReturnValueOnce(first).mockReturnValueOnce(second);
    const props = { title: "Confirm change", message: "Review", onConfirm, onCancel: vi.fn() };
    const view = render(<ConfirmModal {...props} open />);
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    view.rerender(<ConfirmModal {...props} open={false} />);
    view.rerender(<ConfirmModal {...props} open />);
    fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    await act(async () => { finishFirst(); await first; });
    expect(screen.getByRole("dialog").getAttribute("aria-busy")).toBe("true");
    expect(screen.getByRole("button", { name: "Confirm" }).hasAttribute("disabled")).toBe(true);
    await act(async () => { finishSecond(); await second; });
    expect(screen.getByRole("dialog").getAttribute("aria-busy")).toBe("false");
  });
});
