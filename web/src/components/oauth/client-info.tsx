/*
 * How an OAuth Connect app is shown: its logo (https only), name and host,
 * said carefully. A metadata-document app's host vouches for it (a fact); an
 * app that registered itself only claims its name and home page (anyone can
 * register anything), so it reads as unverified. Trusted apps (on the
 * platform's list) are badged.
 */
import type { OAuthClientInfo } from "@/api/oauth";
import { Avatar } from "@/components/ui/avatar/avatar";
import { Badge } from "@/components/ui/badge/badge";
import { CellText } from "@/components/ui/data-table/data-table";
import s from "../../pages/shared.module.css";
import o from "./oauth.module.css";

/** The app's logo, only from an https address (anything else could track or mislead). */
export function safeLogo(client: Pick<OAuthClientInfo, "logoUrl">): string | undefined {
  const url = client.logoUrl;
  if (!url) return undefined;
  try {
    return new URL(url).protocol === "https:" ? url : undefined;
  } catch {
    return undefined;
  }
}

/** "chat.example.com" for a verified app; for one that registered itself, that it did and the host it claims. */
export function clientHostText(client: Pick<OAuthClientInfo, "kind" | "host">): string {
  if (client.kind === "metadata") return client.host || "Verified host";
  return client.host ? `It registered itself and says it's from ${client.host}` : "It registered itself";
}

/** The app's logo, or its initials (decorative: the name is always shown next to it). */
export function AppLogo({ client, size = "sm" }: { client: Pick<OAuthClientInfo, "name" | "logoUrl">; size?: "sm" | "lg" }) {
  const logo = safeLogo(client);
  if (logo) return <img src={logo} alt="" className={o.logo} data-size={size} />;
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
