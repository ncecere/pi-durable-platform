/*
 * The OAuth Connect consent page (/connect/consent, DESIGN.md › OAuth
 * Connect): an app asks to act as the signed-in person. /oauth/authorize
 * checked the request and sent the browser here with its query string; this
 * page reads it again (GET /v1/oauth/consent), shows the app (the host that
 * vouches for a metadata-document app, or "Unverified" for one that
 * registered itself), what it can do, and where the browser goes back to,
 * and sends Allow or Deny (POST, with the CSRF token). The server answers
 * where to go: the app, with a code or access_denied. A request that isn't
 * valid (400 oauth_*, 404) is explained here and never sent anywhere.
 *
 * When the app expects another account (its login_hint), the page offers to
 * sign out and go through the app's request again: signed out, the platform
 * sends the browser to sign in with that hint.
 *
 * No app shell: nothing to wander off to while the app waits. Signed out,
 * the session gate shows the sign-in page, which returns here with the
 * whole query.
 */
import { useMutation, useQuery } from "@tanstack/react-query";
import { useEffect, useId, useState } from "react";
import { ApiError, api, unwrap } from "@/api/client";
import { type OAuthDecision, oauthConsentQuery } from "@/api/oauth";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Card, CardBody } from "@/components/ui/card/card";
import { Stack } from "@/components/ui/layout/layout";
import { Loading } from "@/components/ui/spinner/spinner";
import { InstanceLogo, useCurrentUser, useInstance } from "@/session";
import { ConsentBody } from "./consent-body";
import styles from "./consent.module.css";

/** Leaves the app for the OAuth app (a seam for tests). */
export const redirectTo = { go: (url: string) => window.location.assign(url) };

/** The authorization request's query string as the browser received it, without the "?". */
export const rawQuery = (search: string) => (search.startsWith("?") ? search.slice(1) : search);

/** Only an http(s) address the browser can go back to (the server builds it; anything else is refused here too). */
export function safeRedirect(url: string): string | undefined {
  try {
    const parsed = new URL(url);
    return parsed.protocol === "https:" || parsed.protocol === "http:" ? parsed.href : undefined;
  } catch {
    return undefined;
  }
}

/** Why the request can't go on, in words. */
function invalidReason(err: unknown): string {
  if (err instanceof ApiError && err.status === 404) return "This connection request wasn't found. It may have expired.";
  if (err instanceof ApiError && err.status === 400) return "The app's request isn't valid, so it can't be answered.";
  if (err instanceof ApiError && err.status === 403) return "Your account can't connect apps right now.";
  return "The request couldn't be checked.";
}

export function OAuthConsentPage() {
  const instance = useInstance();
  const me = useCurrentUser();
  // Read once: the address the browser was sent to, untouched by the router.
  const [query] = useState(() => rawQuery(window.location.search));
  const titleId = useId();
  const view = useQuery(oauthConsentQuery(query));
  const [leaving, setLeaving] = useState(false);
  const decide = useMutation({
    mutationFn: async (decision: OAuthDecision) => {
      const out = unwrap(await api.POST("/v1/oauth/consent", { body: { query, decision } }));
      const target = safeRedirect(out.redirectUrl);
      if (!target) throw new Error("The platform answered with an address your browser can't go to, so you weren't sent anywhere.");
      return target;
    },
    onSuccess: (target) => {
      setLeaving(true);
      redirectTo.go(target);
    },
  });
  // Sign out, then the app's request again: the platform sends the browser to sign in, with the app's login_hint.
  const switchAccount = useMutation({
    mutationFn: async () => unwrap(await api.POST("/auth/logout")),
    onSuccess: () => {
      setLeaving(true);
      redirectTo.go(`/oauth/authorize?${query}`);
    },
  });
  const clientName = view.data?.client.name;
  const failed = view.error != null;
  useEffect(() => {
    if (clientName) document.title = `Connect ${clientName} · ${instance.name}`;
    else if (failed) document.title = `Can't connect this app · ${instance.name}`;
  }, [clientName, failed, instance.name]);

  return (
    <main className={styles.page} aria-labelledby={view.data || failed ? titleId : undefined}>
      <div className={styles.column}>
        <div className={styles.brand}>
          {instance.logoUrl && <InstanceLogo src={instance.logoUrl} className={styles.instanceLogo} />}
          <p className={styles.instance}>{instance.name}</p>
        </div>
        <Card>
          <CardBody>
            {view.isLoading && <Loading label="Checking the app's request…" />}
            {failed && (
              <Stack gap={4}>
                <h1 id={titleId} className={styles.title}>
                  Can&apos;t connect this app
                </h1>
                <p className={styles.muted}>{invalidReason(view.error)}</p>
                <ErrorAlert error={view.error} />
                <p className={styles.muted}>
                  Nothing was shared with the app, and this page won&apos;t send you anywhere. Close it and try connecting again from the app.
                </p>
              </Stack>
            )}
            {view.data && <ConsentBody consent={view.data} titleId={titleId} email={me.user.email} decide={decide} leaving={leaving} switchAccount={switchAccount} />}
          </CardBody>
        </Card>
      </div>
    </main>
  );
}
