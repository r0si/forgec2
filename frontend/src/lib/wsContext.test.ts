import { describe, it, expect, vi, afterEach } from "vitest";
import { getWSURL, getHealthURL } from "./wsContext";

describe("getWSURL", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("uses VITE_FORGEC2_WS_URL override as-is", () => {
    vi.stubEnv("VITE_FORGEC2_WS_URL", "wss://ws.example.com");
    expect(getWSURL()).toBe("wss://ws.example.com/ws");
    expect(getWSURL("/custom")).toBe("wss://ws.example.com/custom");
  });

  it("builds ws:// from window.location when no env override", () => {
    const loc = { protocol: "http:", hostname: "c2.internal", port: "8443" };
    Object.defineProperty(window, "location", { configurable: true, value: loc, writable: true });
    vi.stubEnv("VITE_FORGEC2_WS_URL", "");
    vi.stubEnv("VITE_FORGEC2_BACKEND_PORT", "");
    expect(getWSURL()).toBe("ws://c2.internal:8443/ws");
  });

  it("uses wss:// for https pages", () => {
    const loc = { protocol: "https:", hostname: "c2.example", port: "443" };
    Object.defineProperty(window, "location", { configurable: true, value: loc, writable: true });
    vi.stubEnv("VITE_FORGEC2_WS_URL", "");
    vi.stubEnv("VITE_FORGEC2_BACKEND_PORT", "");
    expect(getWSURL()).toBe("wss://c2.example:443/ws");
  });
});

describe("getHealthURL", () => {
  it("maps the socket URL to the HTTP health endpoint", () => {
    const loc = { protocol: "https:", hostname: "c2.example", port: "443" };
    Object.defineProperty(window, "location", { configurable: true, value: loc, writable: true });
    vi.stubEnv("VITE_FORGEC2_WS_URL", "");
    vi.stubEnv("VITE_FORGEC2_BACKEND_PORT", "");
    expect(getHealthURL()).toBe("https://c2.example:443/health");
  });
});