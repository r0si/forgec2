import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { KnowledgeSearchPanel } from "@/pages/ai/components/KnowledgeSearchPanel";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";

vi.mock("@/lib/api", () => ({
  api: { postJson: vi.fn() },
}));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

vi.mock("@/lib/errors", () => ({
  errorText: (_t: unknown, err: unknown) => (err instanceof Error ? err.message : "error"),
}));

describe("KnowledgeSearchPanel", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("refuses to search without a selected collection", () => {
    render(<KnowledgeSearchPanel selectedCollectionIds={[]} />);
    expect(screen.getByText("ai.knowledge_search_pick")).toBeTruthy();
    expect((screen.getByRole("button", { name: /ai\.knowledge_search_go/ }) as HTMLButtonElement).disabled).toBe(true);
    expect(api.postJson).not.toHaveBeenCalled();
  });

  it("posts the selected collections and renders ranked hits in server order", async () => {
    vi.mocked(api.postJson).mockResolvedValue([
      { chunk_id: 2, source_id: 1, source: "runbook.txt", collection_id: 3, chunk_index: 4, content: "rotate krbtgt twice", score: 2.5 },
      { chunk_id: 1, source_id: 1, source: "runbook.txt", collection_id: 3, chunk_index: 0, content: "krbtgt rotation", score: 0.5 },
    ]);
    render(<KnowledgeSearchPanel selectedCollectionIds={[3]} />);

    fireEvent.change(screen.getByLabelText("ai.knowledge_search"), { target: { value: "krbtgt" } });
    fireEvent.click(screen.getByRole("button", { name: /ai\.knowledge_search_go/ }));

    await waitFor(() => expect(api.postJson).toHaveBeenCalledWith(paths.ai.knowledgeSearch, {
      query: "krbtgt",
      collection_ids: [3],
      limit: 6,
    }));
    // Server order is preserved (higher score first), and the score is shown.
    const contents = await screen.findAllByText(/krbtgt/);
    expect(contents[0].textContent).toContain("rotate krbtgt");
    expect(screen.getByText("2.50")).toBeTruthy();
  });

  it("reports a failed search instead of pretending there were no hits", async () => {
    vi.mocked(api.postJson).mockRejectedValue(new Error("boom"));
    render(<KnowledgeSearchPanel selectedCollectionIds={[3]} />);
    fireEvent.change(screen.getByLabelText("ai.knowledge_search"), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: /ai\.knowledge_search_go/ }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("boom");
    // A failed search must not render an empty-results message as if it succeeded.
    expect(screen.queryByText("ai.knowledge_search_empty")).toBeNull();
  });
});
