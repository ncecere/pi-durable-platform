"use client";

import { Separator as BaseSeparator } from "@base-ui/react/separator";
import type { ReactNode } from "react";
import { cx } from "@/lib/bitop-utils";
import styles from "./separator.module.css";

export type SeparatorProps = Omit<BaseSeparator.Props, "className"> & {
  className?: string;
  /** Centred text, e.g. "or". Makes the separator decorative with visible text. */
  label?: ReactNode;
  spacing?: "none" | "sm" | "md" | "lg";
};

export function Separator({ className, label, spacing = "md", orientation = "horizontal", ...props }: SeparatorProps) {
  if (label) {
    return (
      <div className={cx(styles.labelled, className)} data-spacing={spacing}>
        <BaseSeparator {...props} orientation="horizontal" className={styles.line} />
        <span className={styles.label}>{label}</span>
        <BaseSeparator {...props} orientation="horizontal" className={styles.line} />
      </div>
    );
  }
  return (
    <BaseSeparator {...props} orientation={orientation} data-spacing={spacing} className={cx(styles.separator, className)} />
  );
}
