import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { createMemoryRouter, RouterProvider } from "react-router-dom";
import RouterErrorView from "./RouterErrorView";

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

afterEach(cleanup);

it("keeps loader exception details out of the error view and offers navigation", async () => {
  const rawMessage = "internal endpoint failed: token=private-value";
  const router = createMemoryRouter([{
    path: "/broken",
    loader: () => { throw new Error(rawMessage); },
    element: <p>Loaded</p>,
    errorElement: <RouterErrorView />,
    hydrateFallbackElement: <p>Loading</p>,
  }], { initialEntries: ["/broken"] });
  try {
    render(<RouterProvider router={router} />);
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("error.boundary_message");
    expect(alert.textContent).not.toContain(rawMessage);
    expect(screen.getByRole("link", { name: "notfound.back" }).getAttribute("href")).toBe("/dashboard");
    expect(screen.getByRole("button", { name: "error.try_again" })).toBeTruthy();
  } finally {
    router.dispose();
  }
});
