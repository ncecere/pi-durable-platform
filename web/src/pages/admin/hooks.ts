import { useEffect, useState } from "react";
import type { FilterValues } from "@/components/ui/filter-bar/filter-bar";
import { useCurrentUser } from "../../session";

export function useIsPlatformAdmin() {
  return useCurrentUser().capabilities.platformAdmin;
}

export function useDebounced<T>(value: T, ms = 300) {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return v;
}

/** A list facet's single value ("" when unset). */
export const one = (values: FilterValues, id: string) => {
  const v = values[id];
  return Array.isArray(v) ? (v[0] ?? "") : "";
};
