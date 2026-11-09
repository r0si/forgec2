
import { useState, useCallback, useEffect, useMemo, useRef } from "react";
import { useErrorToast } from "@/lib/hooks/useErrorToast";

/**
 * The slice of a schema `useForm` needs, structurally. Zod schemas satisfy it,
 * but so do hand-written validators — the login page used zod for two
 * `min(1)` checks and that pulled the 82 kB zod chunk into the public front
 * door, where it was the largest avoidable script.
 */
export interface FormIssue {
  path: PropertyKey[];
  message: string;
}

export interface FormSchema<T> {
  safeParse: (
    values: T,
  ) => { success: true } | { success: false; error: { issues: FormIssue[] } };
}

interface UseFormOptions<T extends Record<string, unknown>> {
  initialValues: T;
  schema: FormSchema<T>;
  onSubmit: (values: T) => Promise<void>;
}

interface UseFormReturn<T extends Record<string, unknown>> {
  values: T;
  errors: Partial<Record<keyof T, string>>;
  touched: Partial<Record<keyof T, boolean>>;
  isSubmitting: boolean;
  isValid: boolean;
  handleChange: (field: keyof T) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>) => void;
  handleBlur: (field: keyof T) => () => void;
  handleSubmit: (e: React.FormEvent) => Promise<void>;
  setFieldValue: (field: keyof T, value: unknown) => void;
  resetForm: (values?: T) => void;
}

export function useForm<T extends Record<string, unknown>>({
  initialValues: initial,
  schema,
  onSubmit,
}: UseFormOptions<T>): UseFormReturn<T> {
  const [values, setValues] = useState<T>(initial);
  const [errors, setErrors] = useState<Partial<Record<keyof T, string>>>({});
  const [touched, setTouched] = useState<Partial<Record<keyof T, boolean>>>({});
  const [isSubmitting, setIsSubmitting] = useState(false);
  const submittingRef = useRef(false);
  const mountedRef = useRef(true);
  const { toastError } = useErrorToast();

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  const validate = useCallback(
    (vals: T): Partial<Record<keyof T, string>> => {
      const result = schema.safeParse(vals);
      if (result.success) return {};
      // Field names must not collide with inherited properties such as
      // constructor/toString or trigger the legacy __proto__ setter.
      const fieldErrors: Partial<Record<keyof T, string>> = Object.create(null);
      for (const issue of result.error.issues) {
        const path = issue.path[0] as keyof T;
        if (!fieldErrors[path]) fieldErrors[path] = issue.message;
      }
      return fieldErrors;
    },
    [schema],
  );

  const isValid = useMemo(() => Object.keys(validate(values)).length === 0, [validate, values]);

  const handleChange = useCallback(
    (field: keyof T) =>
      (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>) => {
        const input = e.target;
        let newVal: unknown;
        if (input instanceof HTMLInputElement && input.type === "checkbox") {
          newVal = input.checked;
        } else {
          // Keep raw strings for all inputs (including type="number"): zod
          // schemas declare string fields and coerce at submit time via
          // Number()/parseInt(). Coercing here broke z.string() validation
          // ("Expected string, received number") on every number input.
          newVal = input.value;
        }
        setValues((prev) => ({ ...prev, [field]: newVal }));
      },
    [],
  );

  const handleBlur = useCallback(
    (field: keyof T) => () => {
      setTouched((prev) => ({ ...prev, [field]: true }));
      const result = schema.safeParse(values);
      if (!result.success) {
        const issue = result.error.issues.find(
          (i) => i.path[0] === field,
        );
        setErrors((prev) => ({ ...prev, [field]: issue?.message }));
      } else {
        setErrors((prev) => ({ ...prev, [field]: undefined }));
      }
    },
    [schema, values],
  );

  const handleSubmit = useCallback(
    async (e: React.FormEvent) => {
      e.preventDefault();
      if (!mountedRef.current || submittingRef.current) return;
      submittingRef.current = true;
      const allErrors = validate(values);
      setErrors(allErrors);
      setTouched(
        Object.keys(values).reduce(
          (acc, key) => ({ ...acc, [key]: true }),
          {} as Partial<Record<keyof T, boolean>>,
        ),
      );
      if (Object.keys(allErrors).length > 0) {
        submittingRef.current = false;
        return;
      }
      setIsSubmitting(true);
      try {
        await onSubmit(values);
      } catch (err) {
        if (mountedRef.current) toastError(err, "common.submit_failed");
      } finally {
        if (mountedRef.current) setIsSubmitting(false);
        submittingRef.current = false;
      }
    },
    [validate, values, onSubmit, toastError],
  );

  const setFieldValue = useCallback((field: keyof T, value: unknown) => {
    setValues((prev) => ({ ...prev, [field]: value }));
  }, []);

  const resetForm = useCallback(
    (values?: T) => {
      setValues(values ?? initial);
      setErrors({});
      setTouched({});
    },
    [initial],
  );

  return {
    values,
    errors,
    touched,
    isSubmitting,
    isValid,
    handleChange,
    handleBlur,
    handleSubmit,
    setFieldValue,
    resetForm,
  };
}
