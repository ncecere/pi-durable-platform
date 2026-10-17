"use client";

import { Input as BaseInput } from "@base-ui/react/input";
import { X } from "lucide-react";
import { type ComponentPropsWithRef, type KeyboardEvent, useRef, useState } from "react";
import { cx, dataFlag } from "@/lib/bitop-utils";
import styles from "./tag-input.module.css";

/*
 * TagInput: a list of short text tags with an inline input. Enter, comma or
 * Tab (with text) adds a tag; Backspace in an empty input removes the last
 * one; pasting "a, b, c" adds three. Pending text is added on blur, so a tag
 * typed without pressing Enter isn't lost when a form is submitted.
 *
 * The input is a Base UI Field control, so inside <Field> it gets the label,
 * description and invalid state. Each tag has its own "Remove" button, and a
 * polite status message announces additions and removals.
 *
 *   <Field label="Tags" description="Press Enter after each tag.">
 *     <TagInput value={tags} onValueChange={setTags} maxTags={20} />
 *   </Field>
 *
 * `noun` names what the tags are in the list's name and the announcements:
 * noun={{ one: "origin", other: "origins" }} says "Added origin
 * https://www.example.edu" and "Remove origin …" instead of "tag".
 */

export type TagInputProps = Omit<ComponentPropsWithRef<"input">, "className" | "size" | "value" | "defaultValue" | "onChange"> & {
  value: string[];
  onValueChange: (tags: string[]) => void;
  /** Most tags allowed; the input is disabled when reached. */
  maxTags?: number;
  /** Longest tag (characters). */
  maxTagLength?: number;
  /** Cleans a typed tag; return "" to drop it. Default: trim and lower-case. */
  normalize?: (tag: string) => string;
  /** What the tags are, for the list's name, the Remove buttons and the announcements (default "tag" / "tags"). */
  noun?: { one: string; other: string };
  size?: "sm" | "md";
  className?: string;
};

const defaultNormalize = (t: string) => t.trim().toLowerCase();

export function TagInput({
  value,
  onValueChange,
  maxTags,
  maxTagLength = 64,
  normalize = defaultNormalize,
  noun = { one: "tag", other: "tags" },
  size = "md",
  disabled,
  placeholder,
  className,
  onBlur,
  onKeyDown,
  onPaste,
  ref,
  ...props
}: TagInputProps) {
  const [text, setText] = useState("");
  const [message, setMessage] = useState("");
  const inputRef = useRef<HTMLInputElement | null>(null);
  const setRef = (el: HTMLInputElement | null) => {
    inputRef.current = el;
    if (typeof ref === "function") ref(el);
    else if (ref) ref.current = el;
  };
  const full = maxTags !== undefined && value.length >= maxTags;

  function add(raw: string) {
    const next = [...value];
    const added: string[] = [];
    for (const part of raw.split(",")) {
      const tag = normalize(part).slice(0, maxTagLength);
      if (!tag || next.includes(tag)) continue;
      if (maxTags !== undefined && next.length >= maxTags) break;
      next.push(tag);
      added.push(tag);
    }
    setText("");
    if (added.length === 0) return;
    onValueChange(next);
    setMessage(added.length === 1 ? `Added ${noun.one} ${added[0]}` : `Added ${added.length} ${noun.other}`);
  }

  function remove(tag: string) {
    onValueChange(value.filter((t) => t !== tag));
    setMessage(`Removed ${noun.one} ${tag}`);
    inputRef.current?.focus();
  }

  function keyDown(e: KeyboardEvent<HTMLInputElement>) {
    onKeyDown?.(e);
    if (e.defaultPrevented || e.nativeEvent.isComposing) return;
    if ((e.key === "Enter" || e.key === ",") && text.trim()) {
      e.preventDefault();
      add(text);
    } else if (e.key === "Enter") {
      // An empty Enter would submit the surrounding form by accident.
      e.preventDefault();
    } else if (e.key === "Tab" && !e.shiftKey && text.trim()) {
      add(text);
    } else if (e.key === "Backspace" && text === "" && value.length > 0) {
      e.preventDefault();
      remove(value[value.length - 1] ?? "");
    }
  }

  return (
    <div className={cx(styles.root, className)} data-size={size} data-disabled={dataFlag(disabled)} onClick={() => inputRef.current?.focus()}>
      {value.length > 0 && (
        <ul className={styles.tags} aria-label={noun.other.charAt(0).toUpperCase() + noun.other.slice(1)}>
          {value.map((tag) => (
            <li key={tag} className={styles.tag}>
              <span className={styles.tagText}>{tag}</span>
              {!disabled && (
                <button
                  type="button"
                  className={styles.remove}
                  aria-label={`Remove ${noun.one} ${tag}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    remove(tag);
                  }}
                >
                  <X aria-hidden />
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      <BaseInput
        {...(props as BaseInput.Props)}
        ref={setRef}
        value={text}
        disabled={disabled || full}
        maxLength={maxTagLength * 4}
        placeholder={full ? `Limit of ${maxTags} ${noun.other} reached` : (placeholder ?? (noun.one === "tag" ? "Add a tag…" : `Add ${noun.other}…`))}
        autoComplete="off"
        spellCheck={false}
        className={styles.input}
        onValueChange={(v) => setText(v)}
        onKeyDown={keyDown}
        onPaste={(e) => {
          onPaste?.(e as never);
          const pasted = e.clipboardData.getData("text");
          if (pasted.includes(",") || pasted.includes("\n")) {
            e.preventDefault();
            add(text + pasted.replace(/\r?\n/g, ","));
          }
        }}
        onBlur={(e) => {
          onBlur?.(e as never);
          if (text.trim()) add(text);
        }}
      />
      <span role="status" className="sr-only">
        {message}
      </span>
    </div>
  );
}
