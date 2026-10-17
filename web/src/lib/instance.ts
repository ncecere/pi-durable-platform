/*
 * The instance identity: product and organisation names, theme, logo and
 * help link, from GET /v1/auth/config. Nothing in the UI names an
 * organisation; each deployment configures its own.
 *
 * In production the server already writes the theme (<html data-brand>) and
 * the title into index.html, so the first paint is right. applyInstance
 * repeats that in the browser, which is what makes the Vite dev server
 * (serving its own index.html) match.
 */
import type { Schemas } from "../api/client";

export type Instance = Schemas["InstanceInfo"];

export const defaultInstance: Instance = { name: "Pi Durable Platform", orgName: "", theme: "neutral", logoUrl: null, supportUrl: null };

/** The instance from an auth config response, with defaults for anything missing. */
export function instanceOf(config: { instance?: Partial<Instance> | null } | undefined): Instance {
  const i = config?.instance ?? {};
  return {
    name: i.name?.trim() || defaultInstance.name,
    orgName: i.orgName?.trim() ?? "",
    theme: i.theme ?? defaultInstance.theme,
    logoUrl: i.logoUrl || null,
    supportUrl: i.supportUrl || null,
  };
}

/** Sets <html data-brand> and the document title (unless the shell already set a page title ending in the name). */
export function applyInstance(instance: Pick<Instance, "name" | "theme">, doc: Document = document) {
  const root = doc.documentElement;
  if (root.dataset.brand !== instance.theme) root.dataset.brand = instance.theme;
  if (doc.title !== instance.name && !doc.title.endsWith(` \u00b7 ${instance.name}`)) doc.title = instance.name;
}

/** "Durable agents for Example Corp teams" (or "for your teams"). */
export function tagline(instance: Instance) {
  return `Durable agents for ${instance.orgName ? instance.orgName + " teams" : "your teams"}`;
}
