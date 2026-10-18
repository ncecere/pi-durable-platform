/*
 * Trusted apps (OAuth Connect): the words, and the checks that catch a
 * mistyped client ID before Save (the server has the last word, 422
 * invalid_client_id). Only apps identified by a Client ID Metadata Document
 * can be trusted: their client_id is an https address with a path. An app
 * that registered itself gets a new ID each time and anyone can register any
 * name, so it can never be on the list.
 */
import type { TrustedClient } from "@/api/oauth";

/** The API's limits. */
export const maxApps = 50;
export const maxClientId = 500;
export const maxLabel = 100;

const example = "https://chat.example.com/oauth/client-metadata.json";

/** An app as the API returns it: trimmed, without an empty label, in the server's field order. */
export function normalApp(c: TrustedClient): TrustedClient {
  const label = c.label?.trim();
  return { clientId: c.clientId.trim(), ...(label ? { label } : {}) };
}

/** A list's identity for "has it changed". */
export const appsKey = (apps: TrustedClient[]) => JSON.stringify(apps.map((c) => [c.clientId.trim(), c.label?.trim() ?? ""]));

/** What's wrong with a client ID, before it reaches the server. */
export function clientIdProblem(value: string): string | undefined {
  const v = value.trim();
  if (!v) return "Enter the app's client ID: the https address of its metadata document.";
  if (v.length > maxClientId) return `Use at most ${maxClientId} characters.`;
  if (!/^[a-z][a-z0-9+.-]*:/i.test(v)) {
    return `This isn't an https address. Apps that registered themselves can't be trusted (their IDs change); enter a metadata document's address, such as ${example}.`;
  }
  let url: URL;
  try {
    url = new URL(v);
  } catch {
    return `Enter a full https address, such as ${example}.`;
  }
  if (url.protocol !== "https:") return "Only https addresses can be trusted.";
  if (url.username || url.password) return "Remove the user name and password from the address.";
  if (url.hash || v.includes("#")) return "Remove the #fragment: a client ID has none.";
  if (url.pathname === "/" || url.pathname === "") return `Add the document's path: a client ID is its metadata document's address, such as ${example}.`;
  return undefined;
}

/** The host that vouches for an app ("chat.example.com"), shown under its ID. */
export function clientHost(value: string): string | undefined {
  try {
    return new URL(value).host;
  } catch {
    return undefined;
  }
}

/** Why the server refused the list (422 invalid_client_id: details.index and details.reason). */
export const refusedReasons: Record<string, string> = {
  not_metadata_url: "The platform doesn't accept this ID: it must be an https address with a path (a Client ID Metadata Document).",
  duplicate: "This app is listed twice.",
  too_many: `At most ${maxApps} trusted apps.`,
};

export const refusedReason = (reason: unknown) => (typeof reason === "string" && refusedReasons[reason]) || refusedReasons.not_metadata_url!;
