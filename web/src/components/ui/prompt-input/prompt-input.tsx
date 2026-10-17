"use client";

import { ArrowUp, Paperclip, Square } from "lucide-react";
import {
  type ChangeEvent,
  type ComponentPropsWithRef,
  type DragEvent,
  type FormEvent,
  type KeyboardEvent,
  type ReactNode,
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { Attachment, type AttachmentData, Attachments } from "@/components/ui/attachments/attachments";
import { Button, type ButtonProps } from "@/components/ui/button/button";
import { Spinner } from "@/components/ui/spinner/spinner";
import { Tooltip } from "@/components/ui/tooltip/tooltip";
import { cx, dataFlag, matchesAccept } from "@/lib/bitop-utils";
import styles from "./prompt-input.module.css";

/*
 * PromptInput: the chat composer.
 *
 *   <PromptInput onSubmit={({ text, files }) => send(text, files)} status={status} onStop={stop}>
 *     <PromptInputAttachments />
 *     <PromptInputTextarea placeholder="Ask anything…" />
 *     <PromptInputToolbar>
 *       <PromptInputTools><PromptInputAttachButton /></PromptInputTools>
 *       <PromptInputSubmit />
 *     </PromptInputToolbar>
 *   </PromptInput>
 *
 * - A real <form>: Enter submits, Shift+Enter inserts a newline, and Enter
 *   that confirms an IME composition (Japanese, Chinese, Korean…) never
 *   submits. Empty messages and submits while a response is in progress
 *   are ignored.
 * - An uncontrolled textarea is cleared after onSubmit (return false, or
 *   reject, to keep the text). A controlled one (`value`) is yours to clear.
 * - The textarea grows with its content up to a max height, then scrolls.
 * - `maxLength` shows a counter near the limit and announces when it's hit.
 * - PromptInputSubmit follows `status`: Send (ready / error), a spinner
 *   (submitted), Stop (streaming, and submitted when onStop is given).
 * - Focus stays in the textarea after sending, so typing can continue.
 * - `attachments` enables the file picker, paste and drag-and-drop; files
 *   are passed to onSubmit and shown by PromptInputAttachments. Image
 *   previews use object URLs, created only for files that are kept and
 *   revoked when a file is replaced, removed, sent or the composer unmounts.
 * - `disabled` blocks everything: typing, sending, tool buttons, adding
 *   files (picker, paste, drop, addFiles) and removing attachments.
 */

export type ChatStatus = "ready" | "submitted" | "streaming" | "error";

export type PromptInputMessage = { text: string; files: File[] };

export type PromptInputFile = AttachmentData & { file: File };

export type PromptInputFileError = { code: "max_files" | "max_file_size" | "accept"; message: string; file?: File };

type PromptInputContextValue = {
  status: ChatStatus;
  onStop?: () => void;
  disabled: boolean;
  hasText: boolean;
  setHasText: (v: boolean) => void;
  textareaRef: { current: HTMLTextAreaElement | null };
  setControlled: (v: boolean) => void;
  attachments: boolean;
  files: PromptInputFile[];
  addFiles: (files: FileList | File[]) => void;
  removeFile: (id: string) => void;
  openFilePicker: () => void;
};

const PromptInputContext = createContext<PromptInputContextValue | null>(null);

export function usePromptInput() {
  const ctx = useContext(PromptInputContext);
  if (!ctx) throw new Error("PromptInput parts must be inside <PromptInput>");
  return ctx;
}

const isBusy = (s: ChatStatus) => s === "submitted" || s === "streaming";

let fileSeq = 0;
const fileId = () => (typeof crypto !== "undefined" && "randomUUID" in crypto ? crypto.randomUUID() : `file-${++fileSeq}`);

/** Does `file` match an accept string such as "image/*,.pdf,application/json"? (Re-exported from bitop-utils.) */
export { matchesAccept };

export type PromptInputProps = Omit<ComponentPropsWithRef<"form">, "onSubmit"> & {
  onSubmit: (message: PromptInputMessage, event: FormEvent<HTMLFormElement>) => void | boolean | Promise<void | boolean>;
  status?: ChatStatus;
  /** Stops the in-progress response (PromptInputSubmit becomes a Stop button). */
  onStop?: () => void;
  disabled?: boolean;
  /** Enable file attachments: picker, paste and drag-and-drop. */
  attachments?: boolean;
  /** File types for the picker and for pasted / dropped files. */
  accept?: string;
  multiple?: boolean;
  maxFiles?: number;
  /** Bytes. */
  maxFileSize?: number;
  onFileError?: (error: PromptInputFileError) => void;
};

export function PromptInput({
  onSubmit,
  status = "ready",
  onStop,
  disabled = false,
  attachments = false,
  accept,
  multiple = true,
  maxFiles,
  maxFileSize,
  onFileError,
  className,
  children,
  onDragOver,
  onDragLeave,
  onDrop,
  ...props
}: PromptInputProps) {
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const inputRef = useRef<HTMLInputElement | null>(null);
  const controlled = useRef(false);
  const [hasText, setHasText] = useState(false);
  const [files, setFiles] = useState<PromptInputFile[]>([]);
  const [dragging, setDragging] = useState(false);
  const filesRef = useRef(files);
  filesRef.current = files;

  // Revoke preview URLs on unmount.
  useEffect(() => () => filesRef.current.forEach((f) => f.url && URL.revokeObjectURL(f.url)), []);

  /** Replaces the file list, revoking the preview URL of every file that leaves it. */
  const commitFiles = useCallback((next: PromptInputFile[]) => {
    const kept = new Set(next.map((f) => f.id));
    filesRef.current.forEach((f) => f.url && !kept.has(f.id) && URL.revokeObjectURL(f.url));
    filesRef.current = next;
    setFiles(next);
  }, []);

  const addFiles = useCallback(
    (list: FileList | File[]) => {
      if (disabled) return;
      const incoming = Array.from(list);
      let accepted: File[] = [];
      let count = filesRef.current.length;
      for (const file of incoming) {
        if (!matchesAccept(file, accept)) {
          onFileError?.({ code: "accept", message: `${file.name} isn't a supported file type.`, file });
          continue;
        }
        if (maxFileSize !== undefined && file.size > maxFileSize) {
          onFileError?.({ code: "max_file_size", message: `${file.name} is too large.`, file });
          continue;
        }
        if (maxFiles !== undefined && count >= maxFiles) {
          onFileError?.({ code: "max_files", message: `You can attach up to ${maxFiles} files.`, file });
          break;
        }
        count++;
        accepted.push(file);
      }
      if (!accepted.length) return;
      // Single-file mode keeps the first file and replaces the current one.
      if (!multiple) accepted = accepted.slice(0, 1);
      // Preview URLs only for files that are kept (commitFiles revokes replaced ones).
      const added = accepted.map((file): PromptInputFile => {
        const url = file.type.startsWith("image/") && typeof URL.createObjectURL === "function" ? URL.createObjectURL(file) : undefined;
        return { id: fileId(), name: file.name, size: file.size, type: file.type, url, file };
      });
      commitFiles(multiple ? [...filesRef.current, ...added] : added);
    },
    [accept, commitFiles, disabled, maxFiles, maxFileSize, multiple, onFileError],
  );

  const removeFile = useCallback((id: string) => commitFiles(filesRef.current.filter((f) => f.id !== id)), [commitFiles]);

  /**
   * Clears what was sent, and only that: an async onSubmit can resolve after
   * the user has started the next message or attached more files, and that
   * newer draft must survive.
   */
  const clearSubmitted = (text: string, fileIds: Set<string>) => {
    if (filesRef.current.some((f) => fileIds.has(f.id))) commitFiles(filesRef.current.filter((f) => !fileIds.has(f.id)));
    const el = textareaRef.current;
    if (!controlled.current && el && el.value === text) {
      el.value = "";
      el.dispatchEvent(new Event("bitop:reset"));
      setHasText(false);
    }
  };

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (disabled || isBusy(status)) return;
    const text = textareaRef.current?.value ?? "";
    const submitted = filesRef.current;
    if (!text.trim() && submitted.length === 0) return;
    try {
      const result = await onSubmit({ text, files: submitted.map((f) => f.file) }, event);
      if (result !== false) clearSubmitted(text, new Set(submitted.map((f) => f.id)));
    } catch {
      /* keep the draft so the user can retry */
    }
  }

  const acceptsDrop = attachments && !disabled;
  return (
    <PromptInputContext.Provider
      value={{
        status,
        onStop,
        disabled,
        hasText,
        setHasText,
        textareaRef,
        setControlled: (v) => {
          controlled.current = v;
        },
        attachments,
        files,
        addFiles,
        removeFile,
        openFilePicker: () => inputRef.current?.click(),
      }}
    >
      <form
        {...props}
        noValidate
        onSubmit={handleSubmit}
        data-disabled={dataFlag(disabled)}
        data-dragging={dataFlag(dragging)}
        className={cx(styles.root, className)}
        onDragOver={(e: DragEvent<HTMLFormElement>) => {
          onDragOver?.(e);
          if (!acceptsDrop || !e.dataTransfer.types.includes("Files")) return;
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={(e: DragEvent<HTMLFormElement>) => {
          onDragLeave?.(e);
          if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragging(false);
        }}
        onDrop={(e: DragEvent<HTMLFormElement>) => {
          onDrop?.(e);
          if (!acceptsDrop) return;
          e.preventDefault();
          setDragging(false);
          if (e.dataTransfer.files.length) addFiles(e.dataTransfer.files);
        }}
      >
        {attachments && (
          <input
            ref={inputRef}
            type="file"
            hidden
            tabIndex={-1}
            accept={accept}
            multiple={multiple}
            onChange={(e: ChangeEvent<HTMLInputElement>) => {
              if (e.target.files) addFiles(e.target.files);
              e.target.value = "";
              textareaRef.current?.focus();
            }}
          />
        )}
        {children}
      </form>
    </PromptInputContext.Provider>
  );
}

export type PromptInputTextareaProps = Omit<ComponentPropsWithRef<"textarea">, "className"> & {
  className?: string;
  /** Enter submits (default true). Shift+Enter always inserts a newline. */
  submitOnEnter?: boolean;
  /** Counter visibility with `maxLength`: near the limit (auto), always, or never. */
  showCount?: "auto" | "always" | "never";
};

export function PromptInputTextarea({
  className,
  submitOnEnter = true,
  showCount = "auto",
  maxLength,
  value,
  defaultValue,
  onChange,
  onKeyDown,
  onPaste,
  onCompositionStart,
  onCompositionEnd,
  placeholder = "Ask anything…",
  "aria-label": ariaLabel,
  ref,
  disabled,
  ...props
}: PromptInputTextareaProps) {
  const ctx = usePromptInput();
  const countId = useId();
  const composing = useRef(false);
  const local = useRef<HTMLTextAreaElement | null>(null);
  const isControlled = value !== undefined;
  const [length, setLength] = useState(() => String(value ?? defaultValue ?? "").length);
  const { setHasText, setControlled, textareaRef } = ctx;

  useLayoutEffect(() => {
    setControlled(isControlled);
  }, [isControlled, setControlled]);

  const resize = useCallback(() => {
    const el = local.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${el.scrollHeight}px`;
  }, []);

  const sync = useCallback(() => {
    const el = local.current;
    if (!el) return;
    setLength(el.value.length);
    setHasText(el.value.trim().length > 0);
    resize();
  }, [resize, setHasText]);

  // Controlled value changes (including clearing after send).
  useLayoutEffect(sync, [value, sync]);
  // PromptInput clears an uncontrolled textarea after a submit.
  useEffect(() => {
    const el = local.current;
    el?.addEventListener("bitop:reset", sync);
    return () => el?.removeEventListener("bitop:reset", sync);
  }, [sync]);

  const setRefs = (el: HTMLTextAreaElement | null) => {
    local.current = el;
    textareaRef.current = el;
    if (typeof ref === "function") ref(el);
    else if (ref) ref.current = el;
  };

  function handleKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    onKeyDown?.(e);
    if (e.defaultPrevented || e.key !== "Enter") return;
    // IME: the Enter that confirms a composition must not send (keyCode 229 covers Safari).
    if (composing.current || e.nativeEvent.isComposing || e.keyCode === 229) return;
    const modifier = e.metaKey || e.ctrlKey;
    if (e.shiftKey || (!submitOnEnter && !modifier)) return;
    e.preventDefault();
    e.currentTarget.form?.requestSubmit();
  }

  const nearLimit = maxLength !== undefined && length >= Math.floor(maxLength * 0.8);
  const showCounter = maxLength !== undefined && (showCount === "always" || (showCount === "auto" && nearLimit));
  const atLimit = maxLength !== undefined && length >= maxLength;

  return (
    <div className={styles.textareaWrap}>
      <textarea
        {...props}
        ref={setRefs}
        name={props.name ?? "message"}
        rows={props.rows ?? 1}
        value={value}
        defaultValue={defaultValue}
        maxLength={maxLength}
        placeholder={placeholder}
        aria-label={ariaLabel ?? (props["aria-labelledby"] ? undefined : "Message")}
        aria-describedby={cx(props["aria-describedby"], showCounter && countId) || undefined}
        disabled={disabled ?? ctx.disabled}
        className={cx(styles.textarea, className)}
        onChange={(e) => {
          onChange?.(e);
          sync();
        }}
        onKeyDown={handleKeyDown}
        onCompositionStart={(e) => {
          composing.current = true;
          onCompositionStart?.(e);
        }}
        onCompositionEnd={(e) => {
          composing.current = false;
          onCompositionEnd?.(e);
        }}
        onPaste={(e) => {
          onPaste?.(e);
          if (e.defaultPrevented || !ctx.attachments) return;
          const pasted = Array.from(e.clipboardData.files);
          if (pasted.length) {
            e.preventDefault();
            ctx.addFiles(pasted);
          }
        }}
      />
      {showCounter && (
        <span id={countId} className={styles.counter} data-limit={dataFlag(atLimit)}>
          {length.toLocaleString()} / {maxLength!.toLocaleString()}{" "}
          <span className="sr-only">characters</span>
        </span>
      )}
      <span role="status" className="sr-only">
        {atLimit ? `Character limit of ${maxLength} reached` : ""}
      </span>
    </div>
  );
}

export type PromptInputToolbarProps = ComponentPropsWithRef<"div">;

/** The row under the textarea: tools on the left, submit on the right. */
export function PromptInputToolbar({ className, ...props }: PromptInputToolbarProps) {
  return <div {...props} className={cx(styles.toolbar, className)} />;
}

export type PromptInputToolsProps = ComponentPropsWithRef<"div">;

export function PromptInputTools({ className, ...props }: PromptInputToolsProps) {
  return <div {...props} className={cx(styles.tools, className)} />;
}

export type PromptInputButtonProps = Omit<ButtonProps, "iconOnly" | "aria-label" | "children"> & {
  /** Accessible name and tooltip. */
  label: string;
  /** The icon (aria-hidden). */
  children: ReactNode;
  /** Show the label as text next to the icon (no tooltip then). */
  showLabel?: boolean;
  /** Toggle state (e.g. "Search the web" on/off). */
  pressed?: boolean;
};

export function PromptInputButton({
  label,
  children,
  showLabel = false,
  pressed,
  variant = "ghost",
  size = "sm",
  className,
  disabled,
  ...props
}: PromptInputButtonProps) {
  // A disabled PromptInput disables its tool buttons too (usable outside one).
  const ctx = useContext(PromptInputContext);
  const inactive = Boolean(disabled || ctx?.disabled);
  const button = showLabel ? (
    <Button {...props} disabled={inactive} variant={variant} size={size} aria-pressed={pressed} data-pressed={dataFlag(pressed)} className={cx(styles.toolButton, className)}>
      {children}
      <span>{label}</span>
    </Button>
  ) : (
    <Button
      {...props}
      disabled={inactive}
      variant={variant}
      size={size}
      iconOnly
      aria-label={label}
      aria-pressed={pressed}
      data-pressed={dataFlag(pressed)}
      className={cx(styles.toolButton, className)}
    >
      {children}
    </Button>
  );
  return showLabel ? button : <Tooltip content={label}>{button}</Tooltip>;
}

/** Opens the file picker (needs `attachments` on PromptInput). */
export function PromptInputAttachButton({ label = "Attach files", ...props }: Omit<PromptInputButtonProps, "label" | "children"> & { label?: string }) {
  const { openFilePicker, attachments, disabled } = usePromptInput();
  if (!attachments) return null;
  return (
    <PromptInputButton {...props} label={label} disabled={disabled || props.disabled} onClick={openFilePicker}>
      <Paperclip aria-hidden />
    </PromptInputButton>
  );
}

export type PromptInputSubmitProps = Omit<ButtonProps, "iconOnly" | "aria-label" | "children" | "type"> & {
  /** Defaults to PromptInput's status. */
  status?: ChatStatus;
  /** Defaults to PromptInput's onStop. */
  onStop?: () => void;
  /** Labels per state. */
  labels?: Partial<{ send: string; stop: string; sending: string }>;
  /** Show a short visible label next to the icon ("Send" / "Stop") instead of a tooltip. */
  showLabel?: boolean;
  /** The visible labels with showLabel. */
  shortLabels?: Partial<{ send: string; stop: string; sending: string }>;
};

export function PromptInputSubmit({
  status: statusProp,
  onStop: onStopProp,
  labels,
  showLabel = false,
  shortLabels,
  className,
  disabled,
  onClick,
  ...props
}: PromptInputSubmitProps) {
  const ctx = usePromptInput();
  const status = statusProp ?? ctx.status;
  const onStop = onStopProp ?? ctx.onStop;
  const text = { send: "Send message", stop: "Stop generating", sending: "Sending message", ...labels };
  const short = { send: "Send", stop: "Stop", sending: "Sending", ...shortLabels };

  let label = text.send;
  let visible = short.send;
  let icon: ReactNode = <ArrowUp aria-hidden />;
  let type: "submit" | "button" = "submit";
  let inactive = disabled || ctx.disabled || (!ctx.hasText && ctx.files.length === 0);
  let stop = false;
  if (status === "streaming" || (status === "submitted" && onStop)) {
    label = text.stop;
    visible = short.stop;
    icon = status === "submitted" ? <Spinner size="sm" /> : <Square aria-hidden className={styles.stopIcon} />;
    type = "button";
    stop = true;
    inactive = disabled || !onStop;
  } else if (status === "submitted") {
    label = text.sending;
    visible = short.sending;
    icon = <Spinner size="sm" />;
    inactive = true;
  }

  const handleClick: ButtonProps["onClick"] = (e) => {
    onClick?.(e);
    if (stop && !e.defaultPrevented) {
      e.preventDefault();
      onStop?.();
    }
  };

  if (showLabel) {
    return (
      <Button
        {...props}
        type={type}
        variant="primary"
        size="sm"
        aria-label={label}
        data-status={status}
        data-labelled=""
        disabled={inactive}
        className={cx(styles.submit, className)}
        onClick={handleClick}
      >
        {icon}
        <span aria-hidden>{visible}</span>
      </Button>
    );
  }

  return (
    <Tooltip content={label}>
      <Button
        {...props}
        type={type}
        variant="primary"
        size="sm"
        iconOnly
        aria-label={label}
        data-status={status}
        disabled={inactive}
        className={cx(styles.submit, className)}
        onClick={handleClick}
      >
        {icon}
      </Button>
    </Tooltip>
  );
}

/**
 * Chips for the composer's attached files; removing one returns focus to the
 * textarea. While PromptInput is disabled the remove buttons are hidden.
 */
export function PromptInputAttachments({ className }: { className?: string }) {
  const { files, removeFile, textareaRef, disabled } = usePromptInput();
  if (!files.length) return null;
  return (
    <Attachments className={cx(styles.attachments, className)}>
      {files.map((f) => (
        <Attachment
          key={f.id}
          file={f}
          onRemove={
            disabled
              ? undefined
              : () => {
                  removeFile(f.id);
                  textareaRef.current?.focus();
                }
          }
        />
      ))}
    </Attachments>
  );
}
