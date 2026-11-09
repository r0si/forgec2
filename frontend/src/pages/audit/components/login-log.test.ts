import { describe, it, expect } from "vitest";
import { loginEventsToCSV, loginLogFilename, type LoginEvent } from "./login-log";

const ROW: LoginEvent = {
  id: 3,
  user: "alice",
  action: "login",
  ip: "203.0.113.9",
  country: "德国",
  city: "法兰克福",
  success: true,
  details: "Login successful",
  created_at: "2026-11-09T08:30:00Z",
};

describe("loginEventsToCSV", () => {
  it("writes the handover columns in a stable order", () => {
    const csv = loginEventsToCSV([ROW]);
    const lines = csv.trimEnd().split("\r\n");
    expect(lines[0]).toBe('"Time","User","Action","IP","Country","City","Result","Details"');
    expect(lines[1]).toBe(
      '"2026-11-09T08:30:00Z","alice","login","203.0.113.9","德国","法兰克福","success","Login successful"',
    );
  });

  it("keeps the raw country and marks failures", () => {
    const csv = loginEventsToCSV([
      { ...ROW, id: 1, user: "bob", ip: "127.0.0.1", country: "local", city: undefined, success: false, details: "Wrong password" },
    ]);
    // "local" stays machine-readable instead of following the UI language.
    expect(csv).toContain('"local","","failed","Wrong password"');
  });

  it("escapes quotes and neutralises spreadsheet formulas", () => {
    const csv = loginEventsToCSV([
      { ...ROW, user: '=HYPERLINK("http://evil")', details: 'said "hi", then left' },
    ]);
    // A leading '=' must not execute when the file is opened in a spreadsheet.
    expect(csv).toContain('"\'=HYPERLINK(""http://evil"")"');
    expect(csv).toContain('"said ""hi"", then left"');
  });

  it("emits a header row for an empty page", () => {
    expect(loginEventsToCSV([])).toBe(
      '"Time","User","Action","IP","Country","City","Result","Details"\r\n',
    );
  });
});

describe("loginLogFilename", () => {
  it("stamps the export so two files cannot collide", () => {
    expect(loginLogFilename(new Date(2026, 10, 9, 4, 5))).toBe("login-log-20261109-0405.csv");
  });
});
