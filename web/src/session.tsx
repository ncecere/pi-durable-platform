import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, LifeBuoy, LogIn } from "lucide-react";
import { useEffect } from "react";
import { ApiError, api, errorMessage, setCsrfToken, unwrap, type Schemas } from "./api/client";
import { ProductMark } from "./components/layout/product-mark";
import { applyInstance, instanceOf, tagline, type Instance } from "./lib/instance";
import { retryDelay, shouldRetrySession } from "./lib/retry";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Avatar } from "@/components/ui/avatar/avatar";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Card, CardBody } from "@/components/ui/card/card";
import { Stack } from "@/components/ui/layout/layout";
import { Separator } from "@/components/ui/separator/separator";
import { Loading, Spinner } from "@/components/ui/spinner/spinner";
import { TextLink } from "@/components/ui/text-link/text-link";
import styles from "./session.module.css";

export type Me = Schemas["Me"];

/** The signed-in user, or null when signed out. */
export function useMe() {
  return useQuery({
    queryKey: ["me"],
    queryFn: async (): Promise<Me | null> => {
      try {
        // optional: signed out (no session cookie) is data null, not a 401 in the console.
        const me = unwrap(await api.GET("/v1/me", { params: { query: { optional: true } } }));
        if (!me) return null;
        setCsrfToken(me.csrfToken);
        return me;
      } catch (err) {
        if (err instanceof ApiError && err.status === 401) return null;
        throw err;
      }
    },
    staleTime: 60_000,
    // Everything waits for the session: a rate limit is retried after its Retry-After until it lifts.
    retry: shouldRetrySession,
    retryDelay,
  });
}

/** Returns the current user; only call below the signed-in boundary. */
export function useCurrentUser(): Me {
  const { data } = useMe();
  if (!data) throw new Error("useCurrentUser called while signed out");
  return data;
}

export function useAuthConfig() {
  return useQuery({
    queryKey: ["auth-config"],
    queryFn: async () => unwrap(await api.GET("/v1/auth/config")),
    staleTime: Infinity,
  });
}

/** This deployment's name, theme, logo and help link (defaults while loading). */
export function useInstance(): Instance {
  const config = useAuthConfig();
  return instanceOf(config.data);
}

/** Keeps <html data-brand> and the title in line with the instance (see lib/instance.ts). */
export function InstanceSync() {
  const config = useAuthConfig();
  const instance = config.data ? instanceOf(config.data) : undefined;
  const theme = instance?.theme;
  const name = instance?.name;
  useEffect(() => {
    if (theme && name) applyInstance({ theme, name });
  }, [theme, name]);
  return null;
}

/** The instance logo (decorative: the name is always shown next to it). */
export function InstanceLogo({ src, className }: { src: string; className?: string }) {
  return <img src={src} alt="" className={className} />;
}

export function useSignOut() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async () => unwrap(await api.POST("/auth/logout")),
    onSettled: () => {
      setCsrfToken("");
      qc.clear();
      window.location.assign("/");
    },
  });
}

/** A same-site path from ?next= (never another site: "//host" and "/\\host" are refused), or undefined. */
export function nextPath(search: string): string | undefined {
  const n = new URLSearchParams(search).get("next");
  return n && n.startsWith("/") && !n.startsWith("//") && !n.startsWith("/\\") ? n : undefined;
}

export function SignInPage() {
  const config = useAuthConfig();
  const instance = instanceOf(config.data);
  const qc = useQueryClient();
  const devLogin = useMutation({
    mutationFn: async (account: string) => unwrap(await api.POST("/auth/dev", { body: { account } })),
    // From a link that needed a session (?next=): back to that page, now signed in.
    onSuccess: () => (returnTo ? window.location.assign(returnTo) : qc.invalidateQueries({ queryKey: ["me"] })),
  });
  const returnTo = nextPath(window.location.search);
  const next = returnTo ?? window.location.pathname + window.location.search;

  return (
    <main className={styles.page}>
      <div className={styles.column}>
        <div className={styles.brand}>
          {instance.logoUrl ? <InstanceLogo src={instance.logoUrl} className={styles.logo} /> : <ProductMark small className={styles.mark} />}
          <h1 className={styles.title}>{instance.name}</h1>
          <p className={styles.subtitle}>{tagline(instance)}</p>
        </div>
        <Card className={styles.card}>
          <CardBody className={styles.body}>
            {config.isLoading && <Loading label="Loading sign-in options…" />}
            <ErrorAlert error={config.error} />
            {config.data && (
              <Stack gap={5}>
                <div>
                  <h2 className={styles.heading}>Sign in</h2>
                  <p className={styles.lead}>{config.data.oidcEnabled
                      ? `Use your ${instance.orgName ? instance.orgName + " " : "organization "}account to continue.`
                      : "Choose an account to continue."}</p>
                </div>
                {config.data.oidcEnabled && (
                  <Button block render={<a href={"/auth/login?next=" + encodeURIComponent(next)} />}>
                    <LogIn aria-hidden /> Sign in with single sign-on
                  </Button>
                )}
                {config.data.oidcEnabled && config.data.devAuthEnabled && <Separator label="or" spacing="none" />}
                {config.data.devAuthEnabled && (
                  <section aria-labelledby="dev-accounts" className={styles.dev}>
                    <div>
                      <h3 id="dev-accounts" className={styles.devHeading}>
                        Development accounts
                      </h3>
                      <p className={styles.devHint}>Local development only. These accounts have no password.</p>
                    </div>
                    <ul className={styles.accounts}>
                      {config.data.devAccounts.map((a) => {
                        const busy = devLogin.isPending && devLogin.variables === a.id;
                        return (
                          <li key={a.id}>
                            <button
                              type="button"
                              className={styles.account}
                              aria-busy={busy || undefined}
                              disabled={devLogin.isPending}
                              onClick={() => devLogin.mutate(a.id)}
                            >
                              <Avatar name={a.name} size="sm" decorative />
                              <span className={styles.accountText}>
                                <span className={styles.accountName}>{a.name}</span>
                                <span className={styles.accountMeta}>{a.email}</span>
                              </span>
                              {a.platformRole !== "none" && (
                                <Badge size="sm" tone={a.platformRole === "platform_admin" ? "info" : "neutral"}>
                                  {a.platformRole.replace("platform_", "platform ")}
                                </Badge>
                              )}
                              {busy ? <Spinner size="sm" /> : <ArrowRight aria-hidden className={styles.chevron} />}
                            </button>
                          </li>
                        );
                      })}
                    </ul>
                    {devLogin.error && <ErrorAlert error={errorMessage(devLogin.error)} />}
                  </section>
                )}
                {!config.data.oidcEnabled && !config.data.devAuthEnabled && (
                  <Alert tone="warning">No sign-in method is configured.</Alert>
                )}
              </Stack>
            )}
          </CardBody>
        </Card>
        {instance.supportUrl && (
          <p className={styles.help}>
            <TextLink href={instance.supportUrl} target="_blank" rel="noreferrer">
              <LifeBuoy aria-hidden /> Help signing in
            </TextLink>
          </p>
        )}
      </div>
    </main>
  );
}
