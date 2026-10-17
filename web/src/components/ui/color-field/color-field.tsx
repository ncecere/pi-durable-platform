"use client";

import { Input as BaseInput } from "@base-ui/react/input";
import { Check, CircleAlert } from "lucide-react";
import { type CSSProperties, useId } from "react";
import { cx } from "@/lib/bitop-utils";
import styles from "./color-field.module.css";

/*
 * ColorField: a hex colour with a native colour picker, optional preset
 * swatches and a live WCAG contrast check against the text colour that will
 * be drawn on it. Put it inside <Field>: the hex input is the field's
 * control, so the label, description and error attach to it.
 *
 *   <Field label="Accent colour" error={error}>
 *     <ColorField
 *       value={hex}
 *       onValueChange={setHex}
 *       defaultColor="#1d4ed8"
 *       contrastWith="#ffffff"
 *       contrastLabel="white text"
 *       presets={[{ value: "#1d4ed8", label: "Blue" }, { value: "#166534", label: "Green" }]}
 *     />
 *   </Field>
 *
 * The component has no built-in colours: `defaultColor` (used while the
 * value is "") and `contrastWith` are yours, so it fits any brand. The
 * contrast line is the hex input's description (aria-describedby) and says
 * pass/fail in words with the ratio, never colour alone. Ratios are
 * rounded down, so 4.46 never reads as a passing "4.5".
 */

/** "#rrggbb" (lower-case) for "#RGB", "rrggbb" or "#RRGGBB"; undefined when not a colour. */
export function normalizeHex(value: string): string | undefined {
  const v = value.trim().replace(/^#/, "").toLowerCase();
  if (/^[0-9a-f]{6}$/.test(v)) return "#" + v;
  if (/^[0-9a-f]{3}$/.test(v)) return "#" + [...v].map((c) => c + c).join("");
  return undefined;
}

function luminance(hex: string) {
  const n = parseInt(hex.slice(1), 16);
  const channel = (c: number) => {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel((n >> 16) & 255) + 0.7152 * channel((n >> 8) & 255) + 0.0722 * channel(n & 255);
}

/** The WCAG 2.1 contrast ratio of two hex colours (1 to 21), or undefined when either isn't a colour. */
export function contrastRatio(a: string, b: string): number | undefined {
  const x = normalizeHex(a);
  const y = normalizeHex(b);
  if (!x || !y) return undefined;
  const a1 = luminance(x);
  const b1 = luminance(y);
  return (Math.max(a1, b1) + 0.05) / (Math.min(a1, b1) + 0.05);
}

/** "4.5:1", rounded down to one decimal so 4.46 never reads as a passing "4.5". */
export const formatRatio = (r: number) => `${(Math.floor(r * 10) / 10).toFixed(1)}:1`;

export type ColorFieldPreset = { value: string; label: string };

export type ColorFieldProps = {
  /** "#rrggbb", or "" for the default colour. */
  value: string;
  onValueChange: (value: string) => void;
  /** Hex colour used while the value is "" (shown in the swatch and the placeholder). */
  defaultColor: string;
  /** Hex text colour drawn on top of this colour, for the contrast check. */
  contrastWith: string;
  /** How the text colour is described, e.g. "white text". */
  contrastLabel?: string;
  /** Minimum contrast ratio (WCAG AA body text: 4.5). */
  minContrast?: number;
  /** Quick picks, each with an accessible name. */
  presets?: ColorFieldPreset[];
  /** Accessible name of the native colour picker. */
  pickerLabel?: string;
  disabled?: boolean;
  className?: string;
};

export function ColorField({
  value,
  onValueChange,
  defaultColor,
  contrastWith,
  contrastLabel = "the text colour",
  minContrast = 4.5,
  presets = [],
  pickerLabel = "Pick a colour",
  disabled,
  className,
}: ColorFieldProps) {
  const id = useId();
  const hex = normalizeHex(value);
  const fallback = normalizeHex(defaultColor) ?? defaultColor;
  const shown = value === "" ? fallback : (hex ?? fallback);
  const ratio = contrastRatio(shown, contrastWith);
  const passes = ratio !== undefined && ratio >= minContrast;
  const invalid = value !== "" && !hex;
  const text = normalizeHex(contrastWith);
  // Light text needs a darker background to pass, dark text a lighter one.
  const lightText = text ? luminance(text) > 0.18 : true;
  const placeholder = `${fallback} (default)`;
  // Wide enough for the placeholder or a full "#rrggbb" (the font is monospace, so 1ch is one character).
  const hexChars = Math.max(7, placeholder.length);

  return (
    <div className={cx(styles.root, className)}>
      <div className={styles.row}>
        <span className={styles.swatch} style={{ backgroundColor: shown }}>
          <input
            type="color"
            aria-label={pickerLabel}
            className={styles.picker}
            value={shown}
            disabled={disabled}
            onChange={(e) => onValueChange(e.target.value.toLowerCase())}
          />
        </span>
        <BaseInput
          value={value}
          disabled={disabled}
          placeholder={placeholder}
          spellCheck={false}
          autoComplete="off"
          maxLength={7}
          aria-describedby={`${id}-contrast`}
          className={styles.hex}
          style={{ "--color-field-hex-chars": hexChars } as CSSProperties}
          onValueChange={(v) => onValueChange(v.trim().toLowerCase())}
        />
        <span aria-hidden className={styles.sample} style={{ backgroundColor: shown, color: contrastWith }}>
          Aa
        </span>
      </div>
      {presets.length > 0 && (
        <div role="group" aria-label="Preset colours" className={styles.presets}>
          {presets.map((p) => {
            const selected = value !== "" && normalizeHex(p.value) === hex;
            return (
              <button
                key={p.value}
                type="button"
                className={styles.preset}
                style={{ backgroundColor: p.value }}
                aria-label={p.label}
                aria-pressed={selected}
                disabled={disabled}
                title={p.label}
                onClick={() => onValueChange(normalizeHex(p.value) ?? p.value)}
              >
                {selected && <Check aria-hidden />}
              </button>
            );
          })}
        </div>
      )}
      <p id={`${id}-contrast`} className={styles.contrast} data-pass={passes && !invalid ? "" : undefined}>
        {invalid ? (
          <>
            <CircleAlert aria-hidden /> Enter a hex colour such as {fallback}.
          </>
        ) : ratio === undefined ? null : passes ? (
          <>
            <Check aria-hidden /> Contrast with {contrastLabel} {formatRatio(ratio)}: meets WCAG AA ({minContrast}:1).
          </>
        ) : (
          <>
            <CircleAlert aria-hidden /> Contrast with {contrastLabel} {formatRatio(ratio)}: below {minContrast}:1. Choose a{" "}
            {lightText ? "darker" : "lighter"} colour.
          </>
        )}
      </p>
    </div>
  );
}
