import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ActionPicker } from "./ActionPicker";

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string) =>
      ({
        "audit.act.task_create": "Created task",
        "audit.act.agent_delete": "Deleted beacon",
      })[key] ?? key,
  }),
}));

const ACTIONS = [
  { action: "task_create", category: "tasks", count: 12 },
  { action: "operator:agent_delete", category: "agents", count: 3 },
  { action: "listener_create", category: "infrastructure", count: 7 },
];

function renderPicker(props: Partial<React.ComponentProps<typeof ActionPicker>> = {}) {
  return render(
    <ActionPicker
      actions={ACTIONS}
      value=""
      onChange={() => {}}
      allLabel="audit.ops_all_actions"
      placeholder="audit.ops_action_search"
      emptyLabel="audit.ops_action_none"
      {...props}
    />,
  );
}

describe("ActionPicker", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("lists the catalogue with its counts and picks one", async () => {
    const onChange = vi.fn();
    renderPicker({ onChange });

    fireEvent.click(screen.getByRole("button", { name: "audit.ops_all_actions" }));

    expect(await screen.findByText("Created task")).toBeTruthy();
    expect(screen.getByText("12")).toBeTruthy();

    fireEvent.click(screen.getByText("Created task"));
    expect(onChange).toHaveBeenCalledWith("task_create");
  });

  it("filters by the raw code, the label or the category", async () => {
    renderPicker();

    fireEvent.click(screen.getByRole("button", { name: "audit.ops_all_actions" }));
    const search = await screen.findByLabelText("audit.ops_action_search");

    fireEvent.change(search, { target: { value: "agent_delete" } });
    await waitFor(() => expect(screen.queryByText("Created task")).toBeNull());
    expect(screen.getByText("Deleted beacon")).toBeTruthy();

    fireEvent.change(search, { target: { value: "infrastructure" } });
    await waitFor(() => expect(screen.queryByText("Created task")).toBeNull());
    // The dictionary has no entry for this action, so the label is the
    // humanized code.
    expect(screen.getByText(/listener create/i)).toBeTruthy();
  });

  it("says so when nothing matches", async () => {
    renderPicker();

    fireEvent.click(screen.getByRole("button", { name: "audit.ops_all_actions" }));
    const search = await screen.findByLabelText("audit.ops_action_search");
    fireEvent.change(search, { target: { value: "nothing-like-this" } });

    expect(await screen.findByText("audit.ops_action_none")).toBeTruthy();
  });

  it("clears the selection through the all-actions entry", async () => {
    const onChange = vi.fn();
    renderPicker({ value: "task_create", onChange });

    fireEvent.click(screen.getByRole("button", { name: "audit.ops_all_actions" }));
    fireEvent.click(await screen.findByText("audit.ops_all_actions"));
    expect(onChange).toHaveBeenCalledWith("");
  });

  it("shows only the actions of the selected category", async () => {
    renderPicker({ category: "agents" });

    fireEvent.click(screen.getByRole("button", { name: "audit.ops_all_actions" }));
    expect(await screen.findByText("Deleted beacon")).toBeTruthy();
    expect(screen.queryByText("Created task")).toBeNull();
  });
});
