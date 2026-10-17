/*
 * Light and dark: the app follows the system's setting unless the person
 * chooses Light or Dark in the account menu. The choice is kept in this
 * browser (bitop-ui color-mode, localStorage). public/color-mode.js sets
 * <html data-theme> before the first paint; ColorModeSync keeps it in step
 * with the system's setting and with other tabs while the app is open. The
 * sign-in page has no account menu: it follows the system (or a choice made
 * in this browser on this site).
 */
import { Monitor, Moon, Sun } from "lucide-react";
import { setColorMode, useColorMode, type ColorMode } from "@/components/ui/color-mode/color-mode";
import type { CommandGroup } from "@/components/ui/command-palette/command-palette";
import { MenuRadioGroup, MenuRadioItem } from "@/components/ui/menu/menu";
import l from "./layout.module.css";

const MODES: { value: ColorMode; label: string; icon: typeof Sun }[] = [
  { value: "system", label: "System", icon: Monitor },
  { value: "light", label: "Light", icon: Sun },
  { value: "dark", label: "Dark", icon: Moon },
];

const isMode = (v: unknown): v is ColorMode => MODES.some((m) => m.value === v);

/** Follows the system's light or dark setting and other tabs' choices on every page. */
export function ColorModeSync() {
  useColorMode();
  return null;
}

/** System / Light / Dark, in the account menu. */
export function ThemeMenuGroup() {
  const { mode, setMode } = useColorMode();
  return (
    <MenuRadioGroup label="Theme" value={mode} onValueChange={(v) => isMode(v) && setMode(v)}>
      {MODES.map((m) => (
        <MenuRadioItem key={m.value} value={m.value}>
          {/* An icon like the menu's other items (VI2-14). */}
          <span className={l.themeItem}>
            <m.icon aria-hidden />
            {m.label}
          </span>
        </MenuRadioItem>
      ))}
    </MenuRadioGroup>
  );
}

/** "Theme: Dark" and the others in ⌘K (VI2-14), the current one marked. */
export function themeCommands(current: ColorMode): CommandGroup["items"] {
  return MODES.map((m) => ({
    id: `theme:${m.value}`,
    label: `Theme: ${m.label}`,
    icon: <m.icon aria-hidden />,
    hint: m.value === current ? "Current theme" : "Theme",
    keywords: ["theme", "appearance", "dark mode", "light mode", "colour", "color", m.value === "system" ? "follow the system" : `${m.value} mode`],
    onSelect: () => setColorMode(m.value),
  }));
}
