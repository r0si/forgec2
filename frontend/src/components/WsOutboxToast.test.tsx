import { act, render, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { toast } from "sonner";
import WsOutboxToast from "./WsOutboxToast";
import { WS_OUTBOX_DROP_EVENT } from "@/lib/wsContext";
import { I18nProvider } from "@/lib/i18n";

vi.mock("sonner", () => ({
  toast: { warning: vi.fn() },
}));

function renderToast() {
  return render(
    <I18nProvider>
      <WsOutboxToast />
    </I18nProvider>,
  );
}

describe("WsOutboxToast", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it("toasts the cumulative drop count with a stable id", async () => {
    const { unmount } = renderToast();
    try {
      act(() => {
        window.dispatchEvent(new CustomEvent(WS_OUTBOX_DROP_EVENT, { detail: { dropped: 3 } }));
      });
      await waitFor(() => expect(vi.mocked(toast.warning)).toHaveBeenCalledTimes(1));
      const [msg, opts] = vi.mocked(toast.warning).mock.calls[0];
      expect(String(msg)).toContain("3");
      expect(opts).toMatchObject({ id: "ws-outbox-drop" });
      unmount();
    } catch (e) {
      unmount();
      throw e;
    }
  });

  it("stops listening after unmount", () => {
    const { unmount } = renderToast();
    unmount();
    window.dispatchEvent(new CustomEvent(WS_OUTBOX_DROP_EVENT, { detail: { dropped: 1 } }));
    expect(vi.mocked(toast.warning)).not.toHaveBeenCalled();
  });
});
