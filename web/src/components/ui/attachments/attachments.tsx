"use client";

import { File as FileIcon, FileText, ImageIcon, X } from "lucide-react";
import type { ComponentPropsWithRef, ReactNode } from "react";
import { IconButton } from "@/components/ui/button/button";
import { Spinner } from "@/components/ui/spinner/spinner";
import { Tooltip } from "@/components/ui/tooltip/tooltip";
import { cx } from "@/lib/bitop-utils";
import styles from "./attachments.module.css";

/*
 * Attachments: file chips for a composer or a sent message, with an image
 * thumbnail when a preview URL is available, size / type meta, upload state
 * and an optional remove button ("Remove report.pdf").
 *
 *   <Attachments>
 *     {files.map((f) => <Attachment key={f.id} file={f} onRemove={() => remove(f.id)} />)}
 *   </Attachments>
 *
 * PromptInputAttachments (prompt-input) renders these from the composer's
 * own file state. Status is spelled out ("Uploading", "Upload failed") for
 * assistive technology, not only shown as a spinner or colour.
 */

export type AttachmentData = {
  id: string;
  name: string;
  /** Bytes. */
  size?: number;
  /** MIME type, e.g. "image/png". */
  type?: string;
  /** Preview URL (object URL or remote) for images. */
  url?: string;
  status?: "uploading" | "ready" | "error";
};

/** 1536 → "1.5 KB". */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "";
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value >= 10 ? Math.round(value) : Math.round(value * 10) / 10} ${units[unit]}`;
}

function extension(name: string) {
  const dot = name.lastIndexOf(".");
  return dot > 0 ? name.slice(dot + 1).toUpperCase() : undefined;
}

export type AttachmentsProps = ComponentPropsWithRef<"ul"> & {
  /** Accessible name of the list. */
  label?: string;
};

export function Attachments({ label = "Attachments", className, ...props }: AttachmentsProps) {
  return <ul {...props} aria-label={label} className={cx(styles.list, className)} />;
}

export type AttachmentProps = Omit<ComponentPropsWithRef<"li">, "children"> & {
  file: AttachmentData;
  /** Shows a remove button. */
  onRemove?: () => void;
  /** Custom icon instead of the file-type icon. */
  icon?: ReactNode;
};

export function Attachment({ file, onRemove, icon, className, ...props }: AttachmentProps) {
  const isImage = file.type?.startsWith("image/");
  const status = file.status ?? "ready";
  const meta = [extension(file.name), file.size !== undefined ? formatBytes(file.size) : undefined].filter(Boolean).join(" · ");
  const TypeIcon = isImage ? ImageIcon : file.type?.startsWith("text/") || /pdf|word|document/.test(file.type ?? "") ? FileText : FileIcon;
  return (
    <li {...props} data-status={status} className={cx(styles.attachment, className)}>
      <span aria-hidden className={styles.preview}>
        {status === "uploading" ? <Spinner size="sm" /> : isImage && file.url ? <img src={file.url} alt="" className={styles.image} /> : (icon ?? <TypeIcon />)}
      </span>
      <span className={styles.text}>
        <span className={styles.name} title={file.name}>
          {file.name}
        </span>
        <span className={styles.meta}>
          {status === "uploading" ? "Uploading…" : status === "error" ? "Upload failed" : meta}
        </span>
      </span>
      {onRemove && (
        <Tooltip content="Remove">
          <IconButton size="sm" icon={<X aria-hidden />} label={`Remove ${file.name}`} onClick={onRemove} className={styles.remove} />
        </Tooltip>
      )}
    </li>
  );
}
