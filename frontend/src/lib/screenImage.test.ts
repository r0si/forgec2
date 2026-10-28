import { describe, expect, it, vi } from "vitest";
import { dataUrlToBlob, dataUrlToBlobUrl, revokeBlobUrl } from "./screenImage";

// Screenshots arrive from the implant as `data:image/png;base64,...`, and a mis-decode shows up as a
// broken image in the monitor rather than as an error, so the decoding rules are worth pinning.

const bytesOf = async (blob: Blob) => Array.from(new Uint8Array(await blob.arrayBuffer()));

describe("dataUrlToBlob", () => {
  it("decodes a base64 PNG to the exact bytes", async () => {
    // "3q2+7w==" is the four bytes de ad be ef.
    const blob = dataUrlToBlob("data:image/png;base64,3q2+7w==");
    expect(blob).not.toBeNull();
    expect(blob!.type).toBe("image/png");
    expect(await bytesOf(blob!)).toEqual([0xde, 0xad, 0xbe, 0xef]);
  });

  it("decodes a percent-encoded (non-base64) payload as text", async () => {
    // Before the ;base64 check this ran atob("test") and produced garbage bytes.
    const blob = dataUrlToBlob("data:text/plain,test");
    expect(blob).not.toBeNull();
    expect(await bytesOf(blob!)).toEqual([0x74, 0x65, 0x73, 0x74]); // "test"
  });

  it("falls back to image/png when the media type has no subtype", async () => {
    const blob = dataUrlToBlob("data:,abc");
    expect(blob!.type).toBe("image/png");
  });

  it("returns null for a blob: URL (already an object URL)", () => {
    expect(dataUrlToBlob("blob:http://localhost/abc")).toBeNull();
  });

  it("returns null when there is no comma", () => {
    expect(dataUrlToBlob("data:image/png;base64")).toBeNull();
  });

  it("returns null instead of throwing on invalid base64", () => {
    expect(dataUrlToBlob("data:image/png;base64,!!!!")).toBeNull();
  });

  it("returns null instead of throwing on an invalid percent escape", () => {
    expect(dataUrlToBlob("data:text/plain,%E0%A4%A")).toBeNull();
  });
});

describe("dataUrlToBlobUrl", () => {
  it("passes a blob URL through untouched", () => {
    const create = vi.fn();
    vi.stubGlobal("URL", { ...URL, createObjectURL: create });
    expect(dataUrlToBlobUrl("blob:http://localhost/abc")).toBe("blob:http://localhost/abc");
    expect(create).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  it("returns the source when it cannot be decoded, so an <img> still gets something", () => {
    const create = vi.fn(() => "blob:made");
    vi.stubGlobal("URL", { ...URL, createObjectURL: create });
    expect(dataUrlToBlobUrl("not-a-data-url")).toBe("not-a-data-url");
    expect(create).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  it("creates an object URL for a decodable data URL", () => {
    const create = vi.fn(() => "blob:made");
    vi.stubGlobal("URL", { ...URL, createObjectURL: create });
    expect(dataUrlToBlobUrl("data:image/png;base64,3q2+7w==")).toBe("blob:made");
    expect(create).toHaveBeenCalledTimes(1);
    vi.unstubAllGlobals();
  });
});

describe("revokeBlobUrl", () => {
  it("only revokes blob URLs", () => {
    const revoke = vi.fn();
    vi.stubGlobal("URL", { ...URL, revokeObjectURL: revoke });
    revokeBlobUrl("https://example.test/x.png");
    revokeBlobUrl("data:image/png;base64,3q2+7w==");
    expect(revoke).not.toHaveBeenCalled();
    revokeBlobUrl("blob:http://localhost/abc");
    expect(revoke).toHaveBeenCalledWith("blob:http://localhost/abc");
    vi.unstubAllGlobals();
  });

  it("swallows a revoke that throws", () => {
    vi.stubGlobal("URL", {
      ...URL,
      revokeObjectURL: () => {
        throw new Error("already released");
      },
    });
    expect(() => revokeBlobUrl("blob:http://localhost/abc")).not.toThrow();
    vi.unstubAllGlobals();
  });
});
