import { describe, it, expect, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { TooltipProvider } from "@/components/ui/tooltip";
import type { SharedState } from "./types";
import ConnectionPanel from "./ConnectionPanel";

vi.mock("@/lib/i18n", () => ({ useI18n: () => ({ t: (key: string) => key }) }));
vi.mock("@/lib/api", () => ({ api: { get: vi.fn(), delete: vi.fn(), post: vi.fn() } }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const BASE: SharedState = {
  listener_id: "", c2_url: "", protocol: "https", beacon_transport: "https",
  tls_fingerprint: "stable", interval: "15", jitter: "30",
  beacon_randomization: "default", max_random_interval_ms: "",
  ua: "Mozilla/5.0", proxy: "", failover: "", crypto_key: "", beacon_key: "", profile: "",
  dns_doh_url: "", dns_dot_addr: "", ssh_user: "forgec2", ssh_password: "", ssh_key: "", ssh_host_key: "",
  mtls_cert: "", mtls_key: "", mtls_ca: "",
};

function Harness() {
  const [shared, setShared] = useState<SharedState>(BASE);
  return (
    <TooltipProvider delay={0}>
      <ConnectionPanel
        listeners={[]}
        shared={shared}
        profilePresets={[]}
        profileLocked={false}
        showListenerModal={false}
        listenerForm={{ name: "", ltype: "https", host: "", port: "", proto: "" }}
        setShared={setShared}
        changeProfile={() => {}}
        handleCreateListener={() => {}}
        submitListener={() => {}}
        setShowListenerModal={() => {}}
        fileInputRef={{ current: null }}
      />
      <output data-testid="tls-fp">{shared.tls_fingerprint}</output>
    </TooltipProvider>
  );
}

// The select must write the state the build request reads (formData
// tls_fingerprint): a UI that says "randomize" while the payload ships a stable
// fingerprint is the failure this option exists to prevent.
describe("ConnectionPanel TLS fingerprint", () => {
  it("stores the chosen JA3/JA4 option in shared state", async () => {
    render(<Harness />);
    expect(screen.getByTestId("tls-fp").textContent).toBe("stable");

    fireEvent.mouseDown(screen.getByRole("combobox", { name: "generate.tls_fingerprint" }), { button: 0 });
    const option = await screen.findByRole("option", { name: "generate.tls_fp_random" });
    // base-ui commits the item on the pointer pair; a bare click is ignored.
    fireEvent.pointerDown(option);
    fireEvent.pointerUp(option);
    fireEvent.click(option);
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(screen.getByTestId("tls-fp").textContent).toBe("random");
  });

  it("offers pinned browser profiles next to the rotation modes", async () => {
    render(<Harness />);
    fireEvent.mouseDown(screen.getByRole("combobox", { name: "generate.tls_fingerprint" }), { button: 0 });
    expect(await screen.findByRole("option", { name: "generate.tls_fp_stable" })).toBeTruthy();
    expect(screen.getByRole("option", { name: "firefox" })).toBeTruthy();
    expect(screen.getByRole("option", { name: "safari" })).toBeTruthy();
  });
});
