/*
 * The app is waiting out the per-user request limit (AD2-09): once the
 * session has loaded, a 429 is retried after its Retry-After, and until then
 * a list showed "Loading rows…" for up to a minute, which looked hung. This
 * says what is happening and when the next try is, with Try again now.
 */
import { useQueryClient } from "@tanstack/react-query";
import { RefreshCw } from "lucide-react";
import { useEffect, useState, useSyncExternalStore } from "react";
import { Alert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { clearRateLimit, isRateLimited, rateLimitedUntil, subscribeRateLimit } from "../../lib/retry";
import styles from "./banner.module.css";

/** Seconds until `until` (ms since the epoch), ticking while it's ahead; 0 once it passed. */
function useSecondsUntil(until: number) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (until <= Date.now()) return;
    setNow(Date.now());
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [until]);
  return Math.max(0, Math.ceil((until - now) / 1000));
}

export function RateLimitBanner() {
  const qc = useQueryClient();
  const until = useSyncExternalStore(subscribeRateLimit, rateLimitedUntil);
  const left = useSecondsUntil(until);
  // The wait is over: the retry has fired (a new refusal notes the next wait).
  useEffect(() => {
    if (until && left === 0) clearRateLimit();
  }, [until, left]);
  if (!until || left === 0) return null;
  const retryNow = () => {
    clearRateLimit();
    // Refetching a query that waits for its retry starts it now.
    void qc.refetchQueries({ predicate: (q) => isRateLimited(q.state.fetchFailureReason) || isRateLimited(q.state.error) });
    // The banner goes: keep focus on the page rather than the body.
    document.querySelector<HTMLElement>("main")?.focus({ preventScroll: true });
  };
  return (
    <Alert
      tone="warning"
      title="Too many requests: waiting before trying again"
      className={styles.banner}
      actions={
        <Button size="sm" variant="secondary" onClick={retryNow}>
          <RefreshCw aria-hidden /> Try again now
        </Button>
      }
    >
      <p className={styles.reason}>Your account made more requests in the last minute than allowed, for example from several open tabs. Nothing is lost.</p>
      {/* The alert is a polite live region: a ticking countdown would be read out every second, so it says it once. */}
      <p className={styles.detail}>
        <span aria-hidden>Trying again in {left} s.</span>
        <span className="sr-only">It tries again by itself in under a minute.</span>
      </p>
    </Alert>
  );
}
