/*
 * The page shown while the session can't load (AD-03, VI-03): the per-user
 * request limit (429, several tabs or people on one account), a server error
 * or no network. It says what happened in plain words, counts down to the
 * automatic retry of a rate limit (Retry-After), and offers "Try again now".
 * Once the session has loaded, a later failure never replaces the app.
 */
import { useQueryClient } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, errorMessage } from "../../api/client";
import { isRateLimited, MAX_RETRY_AFTER_S } from "../../lib/retry";
import { useInstance } from "../../session";
import { HelpButton } from "../not-found";
import { Button } from "@/components/ui/button/button";
import { Card, CardBody } from "@/components/ui/card/card";
import styles from "./session-unavailable.module.css";

/** Seconds until `deadline` (ms since the epoch), ticking once a second; 0 once it has passed. */
function useSecondsLeft(deadline: number | undefined) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (deadline === undefined) return;
    setNow(Date.now());
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [deadline]);
  return deadline === undefined ? 0 : Math.max(0, Math.ceil((deadline - now) / 1000));
}

type Props = {
  /** The last failure (the query's failureReason or error); render with a new `key` for each failure. */
  error: unknown;
  /** A retry is scheduled (react-query is retrying a rate limit). */
  retrying: boolean;
  onRetry: () => void;
};

export function SessionUnavailable({ error, retrying, onRetry }: Props) {
  const { name, supportUrl, logoUrl } = useInstance();
  // The countdown to the next try starts when this failure shows.
  const [failedAt] = useState(() => Date.now());
  const limited = isRateLimited(error);
  const wait = limited ? Math.min(error.retryAfter ?? 5, MAX_RETRY_AFTER_S) : undefined;
  const left = useSecondsLeft(wait === undefined ? undefined : failedAt + wait * 1000);
  // A rate limit that ran out of retries still comes back by itself once it lifts.
  const auto = limited && !retrying;
  useEffect(() => {
    if (!auto || !wait) return;
    const t = setTimeout(onRetry, Math.max(0, failedAt + wait * 1000 - Date.now()) + 500);
    return () => clearTimeout(t);
  }, [auto, wait, failedAt, onRetry]);

  // After Try again now the page comes back as a new one: focus would fall to the body (AD2-22), so the heading takes it.
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    if (!document.activeElement || document.activeElement === document.body) heading.current?.focus();
  }, []);

  const offline = !(error instanceof ApiError);
  const title = limited ? "Too many requests" : offline ? `Can't reach ${name}` : `${name} isn't responding`;
  const body = limited
    ? "Your account made more requests in the last minute than allowed, for example from several open tabs. Nothing is lost."
    : offline
      ? "Check your connection. Nothing is lost."
      : `${errorMessage(error)} Nothing is lost.`;
  return (
    <main className={styles.page}>
      <Card className={styles.card}>
        <CardBody className={styles.body}>
          {logoUrl && <img src={logoUrl} alt="" className={styles.logo} />}
          <h1 ref={heading} tabIndex={-1} className={styles.title}>
            {title}
          </h1>
          <p className={styles.text}>{body}</p>
          {limited && <p className={styles.countdown}>{left > 0 ? `Trying again in ${left} s.` : "Trying again…"}</p>}
          <div className={styles.actions}>
            <Button onClick={onRetry}>
              <RefreshCw aria-hidden /> {limited ? "Try again now" : "Try again"}
            </Button>
            {supportUrl && <HelpButton href={supportUrl} />}
          </div>
        </CardBody>
      </Card>
    </main>
  );
}

/**
 * The page for a session that hasn't loaded yet and failed (retrying after a
 * rate limit, or given up), or null while it's simply loading.
 */
export function useSessionUnavailable(me: { error: unknown; failureCount: number; failureReason: unknown }) {
  const qc = useQueryClient();
  // Starts over (a refetch would wait for the retry already scheduled): the countdown restarts if it's still refused.
  const retry = useCallback(() => void qc.resetQueries({ queryKey: ["me"], exact: true }), [qc]);
  const failure = me.error ?? (me.failureCount > 0 ? me.failureReason : null);
  // Keyed by the failure count: each new failure restarts the countdown to the next try.
  return failure ? <SessionUnavailable key={me.failureCount} error={failure} retrying={!me.error} onRetry={retry} /> : null;
}
