"use client";

import { Slider as BaseSlider } from "@base-ui/react/slider";
import type { ReactNode } from "react";
import { cx, dataFlag } from "@/lib/bitop-utils";
import styles from "./slider.module.css";

/*
 * Slider: Base UI Slider with a label, an optional value readout and one
 * thumb per value. Pass a number for a single slider or an array for a
 * range. Each thumb is a native <input type="range"> underneath, so arrow
 * keys, Page Up/Down (largeStep), Home and End work and forms submit it.
 *
 *   <Slider label="Volume" defaultValue={40} showValue />
 *   <Slider label="Price" defaultValue={[20, 80]} thumbLabels={["Minimum price", "Maximum price"]} />
 *   <Slider aria-label="Zoom" defaultValue={100} min={25} max={400} step={25} />
 *
 * Naming (required by the type): a visible `label`, an `aria-label`, or
 * `thumbLabels`. Range thumbs without `thumbLabels` are named
 * "<label> minimum" / "<label> maximum" when the label is a string.
 */

type SliderBaseProps = Omit<BaseSlider.Root.Props, "className" | "children" | "render"> & {
  /** Visible label (Slider.Label); names every thumb. */
  label?: ReactNode;
  /** Keep the label for assistive technology only. */
  hideLabel?: boolean;
  /** Accessible name when there is no visible label. */
  "aria-label"?: string;
  /** One accessible name per thumb, e.g. ["Minimum price", "Maximum price"]. */
  thumbLabels?: string[];
  /** Show the current value(s) next to the label (an <output>). */
  showValue?: boolean;
  /** Custom readout, e.g. (values) => `${values[0]}%`. Receives formatted strings and raw numbers. */
  formatValue?: (formatted: readonly string[], values: readonly number[]) => ReactNode;
  /** Screen-reader text for a thumb's value, e.g. "40 percent". */
  getAriaValueText?: (formattedValue: string, value: number, index: number) => string;
  size?: "sm" | "md";
  className?: string;
};

export type SliderProps = SliderBaseProps & ({ label: ReactNode } | { "aria-label": string } | { thumbLabels: string[] });

function thumbName(index: number, count: number, base: string | undefined, thumbLabels: string[] | undefined) {
  const own = thumbLabels?.[index];
  if (own) return own;
  if (!base) return undefined;
  if (count === 1) return base;
  if (count === 2) return `${base} ${index === 0 ? "minimum" : "maximum"}`;
  return `${base} ${index + 1} of ${count}`;
}

export function Slider({
  label,
  hideLabel,
  "aria-label": ariaLabel,
  thumbLabels,
  showValue = false,
  formatValue,
  getAriaValueText,
  size = "md",
  orientation = "horizontal",
  min = 0,
  max = 100,
  value,
  defaultValue,
  className,
  ...props
}: SliderProps) {
  const current = value ?? defaultValue ?? min;
  const count = Array.isArray(current) ? Math.max(current.length, 1) : 1;
  // A visible label names a single thumb via aria-labelledby; ranges need per-thumb names.
  const baseName = ariaLabel ?? (typeof label === "string" ? label : undefined);
  const hasHeader = label !== undefined || showValue;

  return (
    <BaseSlider.Root
      {...props}
      value={value}
      defaultValue={defaultValue}
      min={min}
      max={max}
      orientation={orientation}
      data-size={size}
      className={cx(styles.root, className)}
    >
      {hasHeader && (
        <div className={styles.header} data-hidden-label={dataFlag(hideLabel && !showValue)}>
          {label !== undefined && <BaseSlider.Label className={cx(styles.label, hideLabel && "sr-only")}>{label}</BaseSlider.Label>}
          {showValue && (
            <BaseSlider.Value className={styles.value}>
              {(formatted, values) => (formatValue ? formatValue(formatted, values) : formatted.join(" – "))}
            </BaseSlider.Value>
          )}
        </div>
      )}
      <BaseSlider.Control className={styles.control}>
        <BaseSlider.Track className={styles.track}>
          <BaseSlider.Indicator className={styles.indicator} />
          {Array.from({ length: count }, (_, index) => {
            const name = count === 1 && label !== undefined && !ariaLabel && !thumbLabels?.[0] ? undefined : thumbName(index, count, baseName, thumbLabels);
            return (
              <BaseSlider.Thumb
                key={index}
                index={count > 1 ? index : undefined}
                aria-label={name}
                getAriaValueText={getAriaValueText}
                className={styles.thumb}
              />
            );
          })}
        </BaseSlider.Track>
      </BaseSlider.Control>
    </BaseSlider.Root>
  );
}
