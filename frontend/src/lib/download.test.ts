import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { downloadBlob, downloadJSON, downloadText, downloadFromResponse } from "./download";

// Downloads are fire-and-forget: a wrong filename or a leaked object URL never raises, so these pin
// the mechanics. jsdom has no createObjectURL, hence the stubs.

describe("downloadBlob", () => {
  let created: string[];
  let revoked: string[];
  let clicked: HTMLAnchorElement[];

  beforeEach(() => {
    created = [];
    revoked = [];
    clicked = [];
    vi.stubGlobal("URL", {
      ...URL,
      createObjectURL: (blob: Blob) => {
        created.push(`blob:${blob.size}-${blob.type}`);
        return created[created.length - 1];
      },
      revokeObjectURL: (url: string) => revoked.push(url),
    });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      clicked.push(this);
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("clicks a temporary anchor carrying the download name and the object URL", () => {
    const blob = new Blob(["hello"], { type: "text/plain" });
    downloadBlob(blob, "notes.txt");

    expect(clicked).toHaveLength(1);
    expect(clicked[0].download).toBe("notes.txt");
    expect(clicked[0].href).toContain("blob:");
    // The anchor is cleaned up, not left in the document.
    expect(document.body.contains(clicked[0])).toBe(false);
    // The object URL is released, but only later: revoking immediately breaks the download in some
    // browsers, which is why the implementation waits.
    expect(revoked).toHaveLength(0);
  });

  it("revokes the object URL once the grace period passes", () => {
    vi.useFakeTimers();
    try {
      downloadBlob(new Blob(["x"]), "x.bin");
      expect(revoked).toHaveLength(0);
      vi.advanceTimersByTime(60_000);
      expect(revoked).toEqual(created);
    } finally {
      vi.useRealTimers();
    }
  });

  it("downloadText wraps the content with the requested MIME type", () => {
    downloadText("a,b\n1,2", "data.csv", "text/csv");
    expect(created[0]).toContain("text/csv");
  });

  it("downloadJSON pretty-prints and labels the file as JSON", () => {
    downloadJSON({ a: 1 }, "out.json");
    expect(created[0]).toContain("application/json");
    expect(clicked[0].download).toBe("out.json");
  });
});

describe("downloadFromResponse", () => {
  let names: string[];

  beforeEach(() => {
    names = [];
    vi.stubGlobal("URL", {
      ...URL,
      createObjectURL: () => "blob:x",
      revokeObjectURL: () => void 0,
    });
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      names.push(this.download);
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  const responseWith = (disposition?: string) => {
    const headers = new Headers();
    if (disposition) headers.set("Content-Disposition", disposition);
    return { headers, blob: async () => new Blob(["x"]) } as unknown as Response;
  };

  it("uses the quoted filename from Content-Disposition", async () => {
    await downloadFromResponse(responseWith('attachment; filename="loot.zip"'), "fallback.bin");
    expect(names).toEqual(["loot.zip"]);
  });

  it("uses an unquoted filename", async () => {
    await downloadFromResponse(responseWith("attachment; filename=plugin-7.json"), "fallback.bin");
    expect(names).toEqual(["plugin-7.json"]);
  });

  it("falls back when the header is missing", async () => {
    await downloadFromResponse(responseWith(), "fallback.bin");
    expect(names).toEqual(["fallback.bin"]);
  });

  it("falls back for an RFC 5987 filename* it does not parse", async () => {
    // Recorded as a known limitation rather than silently changing behaviour: the server always
    // sends the quoted form, so filename* would come from somewhere else.
    await downloadFromResponse(
      responseWith("attachment; filename*=UTF-8''%E6%8A%A5%E5%91%8A.xlsx"),
      "fallback.bin",
    );
    expect(names).toEqual(["fallback.bin"]);
  });
});
