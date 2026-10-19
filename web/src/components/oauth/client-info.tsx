/*
 * How an OAuth Connect app is shown: its logo, name and host, said carefully.
 * The logo is loaded through the platform (the page's CSP allows no other
 * image source), with the app's initials when it can't be. A
 * metadata-document app's host vouches for it (a fact); an app that
 * registered itself only claims its name and home page (anyone can register
 * anything), so it reads as unverified. Trusted apps (on the platform's list)
 * are badged.
 */
import { useState } from "react";
import type { OAuthClientInfo } from "@/api/oauth";
import { Avatar } from "@/components/ui/avatar/avatar";
import { Badge } from "@/components/ui/badge/badge";
import { CellText } from "@/components/ui/data-table/data-table";
import s from "../../pages/shared.module.css";
import o from "./oauth.module.css";

/**
 * Where the app's logo is loaded from: the platform, which fetches and checks the logo the app published (only
 * an https one counts: anything else could track or mislead) and serves it from its own origin. Undefined when
 * the app published none.
 */
export function safeLogo(client: Pick<OAuthClientInfo, "id" | "logoUrl">): string | undefined {
  const url = client.logoUrl;
  if (!url) return undefined;
  try {
    if (new URL(url).protocol !== "https:") return undefined;
  } catch {
    return undefined;
  }
  return `/v1/oauth/client-logo?client_id=${encodeURIComponent(client.id)}`;
}

/** "chat.example.com" for a verified app; for one that registered itself, that it did and the host it claims. */
export function clientHostText(client: Pick<OAuthClientInfo, "kind" | "host">): string {
  if (client.kind === "metadata") return client.host || "Verified host";
  return client.host ? `It registered itself and says it's from ${client.host}` : "It registered itself";
}

/** The app's logo, or its initials when it has none or it can't be loaded (decorative: the name is always shown next to it). */
export function AppLogo({ client, size = "sm" }: { client: Pick<OAuthClientInfo, "id" | "name" | "logoUrl">; size?: "sm" | "lg" }) {
  const logo = safeLogo(client);
  const [failed, setFailed] = useState<string>();
  if (logo && failed !== logo) return <img src={logo} alt="" className={o.logo} data-size={size} onError={() => setFailed(logo)} />;
  return <Avatar name={client.name} size={size === "lg" ? "lg" : "sm"} shape="square" decorative />;
}

/** Trusted (skips the consent page) or Unverified (registered itself); nothing for an ordinary verified app. */
export function ClientBadges({ client }: { client: Pick<OAuthClientInfo, "kind" | "trusted"> }) {
  return (
    <>
      {client.trusted && (
        <Badge size="sm" tone="success">
          Trusted
        </Badge>
      )}
      {client.kind === "registered" && (
        <Badge size="sm" tone="warning">
          Unverified
        </Badge>
      )}
    </>
  );
}

/** A table's app cell: logo, name with its badges, and host. */
export function AppCell({ client }: { client: OAuthClientInfo }) {
  return (
    <span className={o.app}>
      <AppLogo client={client} />
      <CellText
        wrap="anywhere"
        primary={
          <span className={s.badges}>
            {client.name}
            <ClientBadges client={client} />
          </span>
        }
        secondary={<span className={client.kind === "metadata" ? s.mono : undefined}>{clientHostText(client)}</span>}
      />
    </span>
  );
}
