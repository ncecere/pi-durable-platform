/*
 * Pi Durable Platform's Wake mark (a team's agents as a 3 x 3 grid of dots, the one awake in the middle larger and
 * fuchsia), shown where an instance has no logo of its own (UI_LOGO_URL). It is the exact brand file from public/, not
 * theme colours, so a theme never recolours the product's logo. Always the tile mark: it reads on the light and the
 * dark theme (the bare mark's fuchsia centre doesn't on light pages). Source: pdp-assets/logo/wake/.
 */
export function ProductMark({ className, small }: { className?: string; small?: boolean }) {
  // small: 20 to 47 px: the sidebar (20 px), the sign-in and session pages (40 px). Otherwise the regular drawing, for 48 px and up.
  return <img src={small ? "/pi-durable-platform-mark-small.svg" : "/pi-durable-platform-mark.svg"} alt="" className={className} />;
}
