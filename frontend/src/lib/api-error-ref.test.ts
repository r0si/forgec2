import { describe, expect, it } from "vitest";
import { ApiError, withErrorRef } from "./api";

// Rounds 238/239 made every server 5xx carry an X-Error-Ref id and a log line tagged with it. This is
// the other half: the id has to reach the operator, or it only exists in a header nobody reads.

describe("withErrorRef", () => {
  it("appends the server reference", () => {
    expect(withErrorRef("Query failed", "abc123")).toBe("Query failed (ref abc123)");
  });

  it("leaves the message alone when there is no reference", () => {
    expect(withErrorRef("name required", null)).toBe("name required");
    expect(withErrorRef("name required", undefined)).toBe("name required");
    expect(withErrorRef("name required", "")).toBe("name required");
  });
});

describe("ApiError", () => {
  it("keeps the reference alongside the status", () => {
    const err = new ApiError("Query failed (ref abc123)", 500, "abc123");
    expect(err.status).toBe(500);
    expect(err.ref).toBe("abc123");
    expect(err.name).toBe("ApiError");
    expect(err.message).toContain("abc123");
  });

  it("works without a reference (4xx)", () => {
    const err = new ApiError("name required", 400);
    expect(err.ref).toBeUndefined();
    expect(err.message).toBe("name required");
  });
});
