import { useState } from "react";

/**
 * State for a flat form object plus a typed setter for one field:
 * `const [form, set] = useFormState({ name: "" }); set("name", "x")`.
 * The fourth value says whether the form differs from how it started (for
 * the "Leave without saving?" guard of a form page or dialog).
 */
export function useFormState<T extends object>(initial: T | (() => T)) {
  const [form, setForm] = useState(initial);
  const [start] = useState(() => JSON.stringify(form));
  const set = <K extends keyof T>(key: K, value: T[K]) => setForm({ ...form, [key]: value });
  const dirty = JSON.stringify(form) !== start;
  return [form, set, setForm, dirty] as const;
}
