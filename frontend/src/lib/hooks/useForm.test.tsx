import { act, fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import type { FormEvent } from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { z } from "zod";

const mocks = vi.hoisted(() => ({ toastError: vi.fn() }));

// The form hook only reports submit failures through the error toast; stub it
// so a rejected onSubmit can be asserted without mounting sonner's toaster.
vi.mock("@/lib/hooks/useErrorToast", () => ({
  useErrorToast: () => ({ toastError: mocks.toastError }),
}));

import { useForm } from "./useForm";

const schema = z.object({
  name: z.string().min(1, "name is required"),
  admin: z.boolean(),
  port: z.string().regex(/^\d+$/, "port must be numeric"),
});

type Values = { name: string; admin: boolean; port: string };

const initial: Values = { name: "", admin: false, port: "" };

function Harness({ onSubmit }: { onSubmit: (values: Values) => Promise<void> }) {
  const form = useForm<Values>({ initialValues: initial, schema, onSubmit });
  return (
    <form onSubmit={form.handleSubmit}>
      <input
        data-testid="name"
        value={form.values.name}
        onChange={form.handleChange("name")}
        onBlur={form.handleBlur("name")}
      />
      <input
        data-testid="admin"
        type="checkbox"
        checked={form.values.admin}
        onChange={form.handleChange("admin")}
      />
      <input
        data-testid="port"
        type="number"
        value={form.values.port}
        onChange={form.handleChange("port")}
      />
      <output data-testid="values">{JSON.stringify(form.values)}</output>
      <output data-testid="valid">{String(form.isValid)}</output>
      <output data-testid="errors">{JSON.stringify(form.errors)}</output>
      <output data-testid="submitting">{String(form.isSubmitting)}</output>
      <button data-testid="set-name" type="button" onClick={() => form.setFieldValue("name", "set!")}>
        set
      </button>
      <button data-testid="reset" type="button" onClick={() => form.resetForm()}>
        reset
      </button>
      <button data-testid="submit" type="submit">
        submit
      </button>
    </form>
  );
}

function values() {
  return JSON.parse(screen.getByTestId("values").textContent!) as Values;
}

function fillValid() {
  fireEvent.change(screen.getByTestId("name"), { target: { value: "agent" } });
  fireEvent.change(screen.getByTestId("port"), { target: { value: "8000" } });
}

describe("useForm", () => {
  beforeEach(() => {
    mocks.toastError.mockClear();
  });

  it("stores text, checkbox and number inputs; numbers stay raw strings", () => {
    render(<Harness onSubmit={vi.fn().mockResolvedValue(undefined)} />);

    fireEvent.change(screen.getByTestId("name"), { target: { value: "abc" } });
    fireEvent.click(screen.getByTestId("admin"));
    fireEvent.change(screen.getByTestId("port"), { target: { value: "42" } });

    // port must remain the string "42": zod declares string fields and coerces
    // at submit time; storing a number here broke validation for every input.
    expect(values()).toEqual({ name: "abc", admin: true, port: "42" });
  });

  it("derives isValid from the schema", () => {
    render(<Harness onSubmit={vi.fn().mockResolvedValue(undefined)} />);
    expect(screen.getByTestId("valid").textContent).toBe("false");

    fillValid();
    expect(screen.getByTestId("valid").textContent).toBe("true");
  });

  it("blur reports the field error and clears it once the value is fixed", () => {
    render(<Harness onSubmit={vi.fn().mockResolvedValue(undefined)} />);

    fireEvent.blur(screen.getByTestId("name"));
    expect(JSON.parse(screen.getByTestId("errors").textContent!)).toEqual({
      name: "name is required",
    });

    fireEvent.change(screen.getByTestId("name"), { target: { value: "ok" } });
    fireEvent.blur(screen.getByTestId("name"));
    expect(JSON.parse(screen.getByTestId("errors").textContent!)).toEqual({});
  });

  it("rejects an invalid submit: every field flagged, onSubmit never called", () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    render(<Harness onSubmit={onSubmit} />);

    fireEvent.click(screen.getByTestId("submit"));

    expect(onSubmit).not.toHaveBeenCalled();
    expect(JSON.parse(screen.getByTestId("errors").textContent!)).toEqual({
      name: "name is required",
      port: "port must be numeric",
    });
  });

  it("awaits onSubmit while submitting and ignores a second click", async () => {
    let resolve!: () => void;
    const onSubmit = vi.fn(
      () =>
        new Promise<void>((done) => {
          resolve = done;
        }),
    );
    render(<Harness onSubmit={onSubmit} />);
    fillValid();

    await act(async () => {
      fireEvent.click(screen.getByTestId("submit"));
    });
    expect(screen.getByTestId("submitting").textContent).toBe("true");
    expect(onSubmit).toHaveBeenCalledTimes(1);

    // The submittingRef guard swallows the double click before validation.
    fireEvent.click(screen.getByTestId("submit"));
    expect(onSubmit).toHaveBeenCalledTimes(1);

    await act(async () => {
      resolve();
    });
    expect(screen.getByTestId("submitting").textContent).toBe("false");
  });

  it("surfaces a rejected submit as a toast and unlocks the form", async () => {
    const failure = new Error("boom");
    const onSubmit = vi.fn().mockRejectedValue(failure);
    render(<Harness onSubmit={onSubmit} />);
    fillValid();

    fireEvent.click(screen.getByTestId("submit"));

    await waitFor(() => {
      expect(screen.getByTestId("submitting").textContent).toBe("false");
    });
    expect(mocks.toastError).toHaveBeenCalledWith(failure, "common.submit_failed");
  });

  it("setFieldValue writes a field and resetForm restores the initial values", () => {
    render(<Harness onSubmit={vi.fn().mockResolvedValue(undefined)} />);

    fireEvent.click(screen.getByTestId("set-name"));
    expect(values().name).toBe("set!");

    fireEvent.click(screen.getByTestId("reset"));
    expect(values()).toEqual(initial);
  });

  it("does not show a stale failure after the form owner unmounts", async () => {
    let reject!: (error: Error) => void;
    const pending = new Promise<void>((_resolve, fail) => { reject = fail; });
    const view = render(<Harness onSubmit={() => pending} />);
    fillValid();
    fireEvent.click(screen.getByTestId("submit"));
    view.unmount();
    await act(async () => {
      reject(new Error("late failure"));
      await pending.catch(() => {});
    });
    expect(mocks.toastError).not.toHaveBeenCalled();
  });

  it("does not submit through a retained handler after unmount", async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    const { result, unmount } = renderHook(() => useForm({
      initialValues: { name: "valid", admin: false, port: "8000" }, schema, onSubmit,
    }));
    const submit = result.current.handleSubmit;
    unmount();
    await submit({ preventDefault: vi.fn() } as unknown as FormEvent);
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("does not lose validation errors for prototype-named fields", async () => {
    const onSubmit = vi.fn().mockResolvedValue(undefined);
    const { result } = renderHook(() => useForm({
      initialValues: { constructor: "", toString: "" },
      schema: z.object({
        constructor: z.string().min(1, "constructor is required"),
        toString: z.string().min(1, "toString is required"),
      }),
      onSubmit,
    }));
    expect(result.current.isValid).toBe(false);
    await act(async () => {
      await result.current.handleSubmit({ preventDefault: vi.fn() } as unknown as FormEvent);
    });
    expect(onSubmit).not.toHaveBeenCalled();
    expect(result.current.errors.constructor).toBe("constructor is required");
    expect(result.current.errors.toString).toBe("toString is required");
  });
});
