import type { ComponentPropsWithRef } from "react";
import { cx } from "@/lib/bitop-utils";
import styles from "./loader.module.css";

/*
 * Loader: three pulsing dots for "waiting for the first token" and other
 * pending states in chat UIs (use Spinner for buttons and page loading).
 * Decorative unless given a `label`, in which case it is a polite status.
 * Under reduced motion the dots stop pulsing and stay visible.
 */

export type LoaderProps = Omit<ComponentPropsWithRef<"span">, "children"> & {
  size?: "sm" | "md" | "lg";
  /** Accessible label; announces the loader as a status. Without it, the loader is decorative. */
  label?: string;
  /** Also show the label next to the dots. */
  showLabel?: boolean;
};

export function Loader({ size = "md", label, showLabel = false, className, ...props }: LoaderProps) {
  return (
    <span
      role={label ? "status" : undefined}
      aria-hidden={label ? undefined : true}
      data-size={size}
      className={cx(styles.loader, className)}
      {...props}
    >
      <span aria-hidden className={styles.dots}>
        <span className={styles.dot} />
        <span className={styles.dot} />
        <span className={styles.dot} />
      </span>
      {label && <span className={showLabel ? styles.label : "sr-only"}>{label}</span>}
    </span>
  );
}
