"use client";

import { Combobox } from "@base-ui/react/combobox";
import { Check, ChevronsUpDown, Search } from "lucide-react";
import { type ReactNode, useMemo, useState } from "react";
import { Badge } from "@/components/ui/badge/badge";
import popup from "@/components/ui/styles/popup.module.css";
import { cx, type Tone } from "@/lib/bitop-utils";
import styles from "./model-selector.module.css";

/*
 * ModelSelector: a searchable model picker grouped by provider, with
 * capability badges. Built on Base UI Combobox with the search input inside
 * the popup (a "searchable select"): the trigger is a button, typing
 * filters, ↑/↓ move, Enter selects, Esc closes and returns focus.
 *
 *   <ModelSelector label="Model" models={models} value={id} onValueChange={setId} />
 */

export type ModelOption = {
  id: string;
  name: string;
  /** Group heading, e.g. "Anthropic". */
  provider: string;
  description?: string;
  /** e.g. ["vision", "tools", "reasoning"]; shown as badges. */
  capabilities?: string[];
  /** Tokens; shown as "200K context". */
  contextWindow?: number;
  /** Decorative provider logo. */
  icon?: ReactNode;
};

export type ModelSelectorProps = {
  models: ModelOption[];
  /** Selected model id (controlled). */
  value?: string | null;
  defaultValue?: string | null;
  onValueChange?: (id: string | null, model: ModelOption | null) => void;
  /** Accessible name, e.g. "Model". */
  label: string;
  placeholder?: string;
  searchPlaceholder?: string;
  emptyText?: ReactNode;
  /** Display names for capability ids, e.g. { vision: "Vision" }. */
  capabilityLabels?: Record<string, string>;
  /** Tones for capability badges that are warnings rather than features, e.g. { failing: "danger" } (default neutral outline). */
  capabilityTones?: Record<string, Tone>;
  disabled?: boolean;
  size?: "sm" | "md";
  className?: string;
};

type Group = { value: string; items: ModelOption[] };

const titleCase = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

function contextLabel(tokens: number) {
  return `${new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 }).format(tokens)} context`;
}

function matches(model: ModelOption, query: string) {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  const hay = [model.name, model.provider, model.id, model.description ?? "", ...(model.capabilities ?? [])].join(" ").toLowerCase();
  return q.split(/\s+/).every((word) => hay.includes(word));
}

export function ModelSelector({
  models,
  value,
  defaultValue = null,
  onValueChange,
  label,
  placeholder = "Select a model",
  searchPlaceholder = "Search models…",
  emptyText = "No models found.",
  capabilityLabels,
  capabilityTones,
  disabled,
  size = "md",
  className,
}: ModelSelectorProps) {
  const [internal, setInternal] = useState<string | null>(defaultValue);
  const selectedId = value !== undefined ? value : internal;
  const selected = models.find((m) => m.id === selectedId) ?? null;
  const groups = useMemo<Group[]>(() => {
    const map = new Map<string, ModelOption[]>();
    for (const m of models) map.set(m.provider, [...(map.get(m.provider) ?? []), m]);
    return [...map].map(([provider, items]) => ({ value: provider, items }));
  }, [models]);
  const cap = (c: string) => capabilityLabels?.[c] ?? titleCase(c);

  return (
    <Combobox.Root
      items={groups}
      value={selected}
      onValueChange={(next: ModelOption | null) => {
        if (value === undefined) setInternal(next?.id ?? null);
        onValueChange?.(next?.id ?? null, next);
      }}
      itemToStringLabel={(m: ModelOption) => m.name}
      itemToStringValue={(m: ModelOption) => m.id}
      isItemEqualToValue={(a: ModelOption, b: ModelOption) => a.id === b.id}
      filter={(m: ModelOption, q: string) => matches(m, q)}
      autoHighlight
      disabled={disabled}
    >
      <Combobox.Trigger className={cx(styles.trigger, className)} data-size={size} aria-label={`${label}: ${selected?.name ?? placeholder}`}>
        {selected?.icon && (
          <span aria-hidden className={styles.logo}>
            {selected.icon}
          </span>
        )}
        <span className={styles.value} data-placeholder={selected ? undefined : ""}>
          {selected?.name ?? placeholder}
        </span>
        <ChevronsUpDown aria-hidden className={styles.chevron} />
      </Combobox.Trigger>
      <Combobox.Portal>
        <Combobox.Positioner className={popup.positioner} align="start" sideOffset={6} collisionPadding={8}>
          <Combobox.Popup className={cx(popup.popup, styles.popup)} aria-label={label}>
            <div className={styles.search}>
              <Search aria-hidden className={styles.searchIcon} />
              <Combobox.Input className={styles.input} placeholder={searchPlaceholder} aria-label={`Search ${label.toLowerCase()}s`} />
            </div>
            <Combobox.Empty className={styles.empty}>{emptyText}</Combobox.Empty>
            <Combobox.List className={styles.list} aria-label={label}>
              {(group: Group) => (
                <Combobox.Group key={group.value} items={group.items} className={styles.group}>
                  <Combobox.GroupLabel className={popup.groupLabel}>{group.value}</Combobox.GroupLabel>
                  <Combobox.Collection>
                    {(m: ModelOption) => (
                      <Combobox.Item key={m.id} value={m} className={cx(popup.item, styles.item)}>
                        <span aria-hidden className={styles.itemLogo}>
                          {m.icon}
                        </span>
                        <span className={styles.itemText}>
                          <span className={styles.itemName}>{m.name}</span>
                          {(m.description || m.contextWindow) && (
                            <span className={styles.itemMeta}>
                              {[m.description, m.contextWindow ? contextLabel(m.contextWindow) : undefined].filter(Boolean).join(" · ")}
                            </span>
                          )}
                        </span>
                        {m.capabilities?.length ? (
                          <span className={styles.badges}>
                            {m.capabilities.map((c) => (
                              <Badge key={c} size="sm" variant={capabilityTones?.[c] ? "soft" : "outline"} tone={capabilityTones?.[c]}>
                                {cap(c)}
                              </Badge>
                            ))}
                          </span>
                        ) : null}
                        <Combobox.ItemIndicator className={styles.check}>
                          <Check aria-hidden />
                        </Combobox.ItemIndicator>
                      </Combobox.Item>
                    )}
                  </Combobox.Collection>
                </Combobox.Group>
              )}
            </Combobox.List>
          </Combobox.Popup>
        </Combobox.Positioner>
      </Combobox.Portal>
    </Combobox.Root>
  );
}
