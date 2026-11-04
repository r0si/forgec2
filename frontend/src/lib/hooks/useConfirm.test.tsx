import { describe, it, expect, vi } from "vitest";
import { render, renderHook, screen, fireEvent, act } from "@testing-library/react";
import type { ReactElement } from "react";
import { useConfirm } from "./useConfirm";

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

function Harness({ onResult }: { onResult: (v: boolean) => void }) {
  const { confirm, modal } = useConfirm();
  return (
    <div>
      <button onClick={() => { confirm({ message: "Proceed?", danger: true }).then(onResult); }}>
        open
      </button>
      {modal}
    </div>
  );
}


describe("useConfirm", () => {
  it("resolves true when confirmed", async () => {
    const onResult = vi.fn();
    render(<Harness onResult={onResult} />);
    fireEvent.click(screen.getByText("open"));
    expect(await screen.findByText("Proceed?")).toBeDefined();
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Confirm" }));
    });
    expect(onResult).toHaveBeenCalledWith(true);
  });

  it("resolves false when cancelled", async () => {
    const onResult = vi.fn();
    render(<Harness onResult={onResult} />);
    fireEvent.click(screen.getByText("open"));
    await screen.findByText("Proceed?");
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    });
    expect(onResult).toHaveBeenCalledWith(false);
  });

  it("renders nothing before a confirm is requested", () => {
    render(<Harness onResult={vi.fn()} />);
    expect(screen.queryByText("Proceed?")).toBeNull();
  });

  it("requires typed text to match requireText before enabling confirm", async () => {
    const onResult = vi.fn();
    function TypedHarness() {
      const { confirm, modal } = useConfirm();
      return (
        <div>
          <button onClick={() => { confirm({ message: "Type to confirm", requireText: "delete" }).then(onResult); }}>
            open
          </button>
          {modal}
        </div>
      );
    }
    render(<TypedHarness />);
    fireEvent.click(screen.getByText("open"));
    await screen.findByText("Type to confirm");
    const confirmBtn = screen.getByRole("button", { name: "Confirm" }) as HTMLButtonElement;
    expect(confirmBtn.disabled).toBe(true);
    await act(async () => {
      fireEvent.change(screen.getByLabelText(/to confirm/i), { target: { value: "delete" } });
    });
    expect(confirmBtn.disabled).toBe(false);
    await act(async () => {
      fireEvent.click(confirmBtn);
    });
    expect(onResult).toHaveBeenCalledWith(true);
  });

  it("cancels an outstanding request when its owner unmounts", async () => {
    const settled = vi.fn();
    const { result, unmount } = renderHook(() => useConfirm());
    act(() => { void result.current.confirm({ message: "Pending" }).then(settled); });
    unmount();
    await act(async () => {});
    expect(settled).toHaveBeenCalledExactlyOnceWith(false);
  });

  it("does not create a pending request through a retained callback after unmount", async () => {
    const settled = vi.fn();
    const { result, unmount } = renderHook(() => useConfirm());
    const confirm = result.current.confirm;
    unmount();
    void confirm({ message: "Too late" }).then(settled);
    await act(async () => {});
    expect(settled).toHaveBeenCalledExactlyOnceWith(false);
  });

  it("does not let a stale prompt approve its replacement", async () => {
    const first = vi.fn();
    const second = vi.fn();
    const { result } = renderHook(() => useConfirm());
    act(() => { void result.current.confirm({ message: "First" }).then(first); });
    const oldModal = result.current.modal as ReactElement<{ onConfirm: () => void }>;
    act(() => { void result.current.confirm({ message: "Second" }).then(second); });
    await act(async () => { oldModal.props.onConfirm(); });
    expect(first).toHaveBeenCalledExactlyOnceWith(false);
    expect(second).not.toHaveBeenCalled();
    expect(result.current.modal).not.toBeNull();
  });

  it("requires fresh typed confirmation when a pending prompt is replaced", async () => {
    function ReplacementHarness() {
      const { confirm, modal } = useConfirm();
      return <>
        <button onClick={() => { void confirm({ message: "First", requireText: "delete" }); }}>first</button>
        <button onClick={() => { void confirm({ message: "Second", requireText: "delete" }); }}>second</button>
        {modal}
      </>;
    }
    render(<ReplacementHarness />);
    fireEvent.click(screen.getByText("first"));
    fireEvent.change(screen.getByLabelText(/to confirm/i), { target: { value: "delete" } });
    expect(screen.getByRole("button", { name: "Confirm" }).hasAttribute("disabled")).toBe(false);
    fireEvent.click(screen.getByText("second"));
    await screen.findByText("Second");
    expect((screen.getByRole("textbox") as HTMLInputElement).value).toBe("");
    expect(screen.getByRole("button", { name: "Confirm" }).hasAttribute("disabled")).toBe(true);
  });
});
