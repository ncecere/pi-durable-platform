"use client";

import { Progress as BaseProgress } from "@base-ui/react/progress";
import type { ReactNode } from "react";
import { cx } from "@/lib/bitop-utils";
import styles from "./progress.module.css";

export type ProgressProps = {
  /** Visible label; also the progressbar's accessible name. */
  label: ReactNode;
  hideLabel?: boolean;
  /** 0–max, or null for indeterminate. */
  value: number | null;
  max?: number;
  /** Show the formatted value (e.g. "40%") on the right. */
  showValue?: boolean;
  tone?: "primary" | "success" | "danger";
  size?: "sm" | "md";
  className?: string;
};

/** A labelled progress bar (Base UI Progress: role="progressbar" with aria-value*). */
export function Progress({ label, hideLabel, value, max = 100, showValue = true, tone = "primary", size = "md", className }: ProgressProps) {
  return (
    <BaseProgress.Root value={value} max={max} className={cx(styles.root, className)} data-tone={tone} data-size={size}>
      <div className={cx(styles.header, hideLabel && !showValue && "sr-only")}>
        <BaseProgress.Label className={cx(styles.label, hideLabel && "sr-only")}>{label}</BaseProgress.Label>
        {showValue && value !== null && <BaseProgress.Value className={styles.value} />}
      </div>
      <BaseProgress.Track className={styles.track}>
        <BaseProgress.Indicator className={styles.indicator} />
      </BaseProgress.Track>
    </BaseProgress.Root>
  );
}
