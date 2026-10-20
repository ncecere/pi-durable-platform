/*
 * The consent page's question: the app (logo, name, host, Trusted or
 * Unverified), what it will be able to do (the agents or agents:read scope
 * in plain words; it acts as the person, never with admin powers), where the
 * browser goes back to, and Deny / Allow. When the app expects another
 * account than the one signed in (login_hint), it says so and offers only to
 * cancel or to sign in as that account: Allow would connect the wrong one.
 */
import { Check, ShieldOff } from "lucide-react";
import type { OAuthConsent, OAuthDecision } from "@/api/oauth";
import { AppLogo, ClientBadges, clientHostText } from "@/components/oauth/client-info";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { Stack } from "@/components/ui/layout/layout";
import { useInstance } from "@/session";
import styles from "./consent.module.css";

type Props = {
  consent: OAuthConsent;
  titleId: string;
  email: string;
  decide: { mutate: (d: OAuthDecision) => void; isPending: boolean; variables?: OAuthDecision; error: unknown };
  /** The answer was sent and the browser is on its way back to the app. */
  leaving: boolean;
  /** Signs out and starts the app's request again, to sign in as the account it expects. */
  switchAccount: { mutate: () => void; isPending: boolean; error: unknown };
};

/** What a scope lets the app do, one line each (an unknown scope by its name). */
function abilities(scope: string, personal?: OAuthConsent["personalWorkspace"]): string[] {
  if (scope === "agents:read") return ["See your agents and read your conversations with them", "Follow your conversations as they change"];
  if (scope !== "agents") return [`Use the “${scope}” permission`];
  return [
    "See your agents and chat with them",
    personal ? `Create agents in your personal workspace (${personal.name}) and in teams where you can` : "Create agents in teams where you can",
  ];
}

export function ConsentBody({ consent, titleId, email, decide, leaving, switchAccount }: Props) {
  const instance = useInstance();
  const { client } = consent;
  const unverified = client.kind === "registered";
  const busy = decide.isPending || leaving || switchAccount.isPending;
  const scopes = consent.scopes.length > 0 ? consent.scopes : ["agents"];
  const readOnly = scopes.every((sc) => sc === "agents:read");
  const mismatch = consent.accountMismatch === true;
  return (
    <Stack gap={5}>
      <div className={styles.client}>
        <AppLogo client={client} size="lg" />
        <div className={styles.clientText}>
          <h1 id={titleId} className={styles.title}>
            {client.name} wants to {readOnly ? "read" : "use"} your agents as you
          </h1>
          <p className={styles.host}>
            <ClientBadges client={client} />
            {unverified ? (
              <span>{clientHostText(client)}</span>
            ) : (
              <span>
                From <strong>{client.host}</strong>
              </span>
            )}
          </p>
        </div>
      </div>
      {unverified ? (
        <Alert tone="warning">
          This app registered itself, so {instance.name} can&apos;t verify who made it: its name and website aren&apos;t checked. Allow it only if you just
          connected it yourself.
        </Alert>
      ) : (
        <p className={styles.muted}>
          Its details come from a document published at <strong>{client.host}</strong>, which vouches for it.
          {client.trusted && ` ${instance.name} trusts this app.`}
        </p>
      )}
      {consent.remembered && <Alert tone="info">You allowed this app before. Allowing again signs it in again.</Alert>}
      <section aria-labelledby={`${titleId}-can`}>
        <h2 id={`${titleId}-can`} className={styles.heading}>
          It will be able to
        </h2>
        <ul className={styles.list}>
          {scopes.flatMap((sc) => abilities(sc, consent.personalWorkspace)).map((line) => (
            <li key={line}>
              <Check aria-hidden /> {line}
            </li>
          ))}
          <li>
            <Check aria-hidden /> Act as you, with your access: what it does is recorded under your name
          </li>
          {readOnly && (
            <li data-tone="limit">
              <ShieldOff aria-hidden /> Never send messages or change anything
            </li>
          )}
          <li data-tone="limit">
            <ShieldOff aria-hidden /> Never with admin powers, even if you have them
          </li>
        </ul>
        <p className={styles.muted}>You can disconnect it at any time from Account › Connected apps.</p>
      </section>
      <p className={styles.muted}>
        Signed in as <strong>{email}</strong>. You&apos;ll be sent back to <strong>{consent.redirectHost}</strong>.
      </p>
      {mismatch && (
        <Alert tone="warning" title="This isn't the account the app expects">
          {client.name} asked to connect <strong>{consent.loginHint}</strong>, but you&apos;re signed in as <strong>{email}</strong>. Sign in as{" "}
          {consent.loginHint} to connect it, or cancel.
        </Alert>
      )}
      {decide.error != null && <ErrorAlert error={decide.error} title="Couldn't send your answer" />}
      {switchAccount.error != null && <ErrorAlert error={switchAccount.error} title="Couldn't sign you out" />}
      <p role="status" className={styles.muted}>
        {leaving ? `Taking you back to ${consent.redirectHost}…` : ""}
      </p>
      <div className={styles.actions}>
        <Button variant="secondary" disabled={busy} loading={decide.isPending && decide.variables === "deny"} onClick={() => decide.mutate("deny")}>
          {mismatch ? "Cancel" : "Deny"}
        </Button>
        {mismatch ? (
          <Button disabled={busy} loading={switchAccount.isPending} onClick={() => switchAccount.mutate()}>
            Sign in as {consent.loginHint}
          </Button>
        ) : (
          <Button disabled={busy} loading={decide.isPending && decide.variables === "allow"} onClick={() => decide.mutate("allow")}>
            Allow
          </Button>
        )}
      </div>
    </Stack>
  );
}
