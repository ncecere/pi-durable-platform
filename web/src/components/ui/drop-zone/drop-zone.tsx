"use client";

import { Upload } from "lucide-react";
import { type DragEvent, type ReactNode, useId, useRef, useState } from "react";
import { Button } from "@/components/ui/button/button";
import { cx, dataFlag, matchesAccept } from "@/lib/bitop-utils";
import styles from "./drop-zone.module.css";

/*
 * DropZone: drag-and-drop area plus a real button that opens the file
 * picker, so it works with keyboard, switch and screen-reader users (the
 * drop target itself is a pointer-only enhancement). The area is a labelled
 * group; the button is described by the hint text.
 *
 * Chosen and dropped files are both checked against `accept` and `maxSize`
 * (the picker's accept is only a hint, and drops ignore it). Rejected files
 * go to `onReject` and are announced in a polite status line under the
 * button, e.g. "setup.exe isn't an accepted file type."
 */

export type DropZoneRejection = { file: File; reason: "type" | "size" };

export type DropZoneProps = {
  /** Called with the accepted chosen or dropped files (never empty). */
  onFiles: (files: File[]) => void;
  /** Called with the files that failed `accept` ("type") or `maxSize` ("size"). */
  onReject?: (rejections: DropZoneRejection[]) => void;
  /** Group heading, e.g. "Upload documents". */
  label?: ReactNode;
  /** Visible button text; also its accessible name. */
  buttonLabel?: string;
  /** Hint text (accepted types, limits); describes the button. */
  description?: ReactNode;
  /** Accepted types, e.g. ".pdf,.docx" or "image/*": filters both picked and dropped files. */
  accept?: string;
  /** Largest accepted file, in bytes. */
  maxSize?: number;
  multiple?: boolean;
  disabled?: boolean;
  /** An upload is running: the button shows a spinner. */
  busy?: boolean;
  icon?: ReactNode;
  className?: string;
  children?: ReactNode;
};

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${Number(value.toFixed(1))} ${units[unit]}`;
}

function rejectionMessage({ file, reason }: DropZoneRejection, maxSize: number | undefined): string {
  return reason === "type"
    ? `${file.name} isn't an accepted file type.`
    : `${file.name} is larger than ${maxSize === undefined ? "the limit" : formatSize(maxSize)}.`;
}

export function DropZone({
  onFiles,
  onReject,
  label = "Drag and drop files here, or",
  buttonLabel = "Choose files",
  description,
  accept,
  maxSize,
  multiple = true,
  disabled = false,
  busy = false,
  icon,
  className,
  children,
}: DropZoneProps) {
  const id = useId();
  const input = useRef<HTMLInputElement>(null);
  const [dragging, setDragging] = useState(false);
  const [rejected, setRejected] = useState<string>("");
  const inactive = disabled || busy;

  function deliver(list: FileList | null | undefined) {
    const files = [...(list ?? [])];
    if (files.length === 0 || inactive) return;
    const accepted: File[] = [];
    const rejections: DropZoneRejection[] = [];
    for (const file of files) {
      if (!matchesAccept(file, accept)) rejections.push({ file, reason: "type" });
      else if (maxSize !== undefined && file.size > maxSize) rejections.push({ file, reason: "size" });
      else accepted.push(file);
    }
    setRejected(rejections.map((r) => rejectionMessage(r, maxSize)).join(" "));
    if (rejections.length) onReject?.(rejections);
    if (accepted.length) onFiles(multiple ? accepted : accepted.slice(0, 1));
  }

  const onDragOver = (e: DragEvent) => {
    e.preventDefault();
    if (!inactive) setDragging(true);
  };
  const onDrop = (e: DragEvent) => {
    e.preventDefault();
    setDragging(false);
    deliver(e.dataTransfer?.files);
  };

  return (
    <div
      role="group"
      aria-labelledby={`${id}-label`}
      data-dragging={dataFlag(dragging)}
      data-disabled={dataFlag(disabled)}
      className={cx(styles.zone, className)}
      onDragOver={onDragOver}
      onDragEnter={onDragOver}
      onDragLeave={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragging(false);
      }}
      onDrop={onDrop}
    >
      <span aria-hidden className={styles.icon}>
        {icon ?? <Upload />}
      </span>
      <p id={`${id}-label`} className={styles.label}>
        {label}
      </p>
      <Button
        variant="secondary"
        loading={busy}
        disabled={disabled}
        aria-describedby={description ? `${id}-desc` : undefined}
        onClick={() => input.current?.click()}
      >
        {buttonLabel}
      </Button>
      <input
        ref={input}
        type="file"
        hidden
        tabIndex={-1}
        multiple={multiple}
        accept={accept}
        disabled={inactive}
        aria-label={buttonLabel}
        onChange={(e) => {
          const files = e.target.files;
          deliver(files);
          e.target.value = "";
        }}
      />
      {description && (
        <p id={`${id}-desc`} className={styles.description}>
          {description}
        </p>
      )}
      {/* Always rendered so screen readers pick up changes (a live region must exist first). */}
      <p role="status" className={styles.rejected} data-empty={dataFlag(!rejected)}>
        {rejected}
      </p>
      {children}
    </div>
  );
}
