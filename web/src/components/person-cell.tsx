/* A table cell body with an avatar next to a name and a secondary line (people and teams). */
import type { ReactNode } from "react";
import { Avatar, type AvatarProps } from "@/components/ui/avatar/avatar";
import s from "../pages/shared.module.css";

/** `children` is the text next to the avatar, usually a primary name and a secondary line. */
export function PersonCell({ name, shape, children }: { name: string; shape?: AvatarProps["shape"]; children: ReactNode }) {
  return (
    <span className={s.person}>
      <Avatar name={name} shape={shape} size="sm" decorative />
      <span className={s.personText}>{children}</span>
    </span>
  );
}
