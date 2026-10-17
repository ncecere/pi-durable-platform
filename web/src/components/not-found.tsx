/*
 * The not-found page: unknown addresses, and unknown teams or objects (a
 * detail page whose API call returns 404). Use `isNotFound(error)` to decide,
 * then render <NotFoundState what="team" /> instead of an inline error.
 */
import { Link, useRouterState } from "@tanstack/react-router";
import { Home, LifeBuoy, SearchX, ShieldAlert, Shield } from "lucide-react";
import { ApiError } from "../api/client";
import { useCurrentUser, useInstance } from "../session";
import s from "../pages/shared.module.css";
import { useCrumbTail } from "./layout/crumb-tail";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";

/** The API said the object doesn't exist (or isn't visible to the caller), or the id in the address isn't one (invalid_id). */
export const isNotFound = (err: unknown) => err instanceof ApiError && (err.status === 404 || (err.status === 400 && err.code === "invalid_id"));

/** A "Help" link to SUPPORT_URL, for error pages. */
export function HelpButton({ href }: { href: string }) {
  return (
    <Button variant="secondary" render={<a href={href} target="_blank" rel="noreferrer" />}>
      <LifeBuoy aria-hidden /> Help
    </Button>
  );
}

const titles = {
  page: { title: "Page not found", body: "There's nothing at this address." },
  team: { title: "Team not found", body: "There's no team at this address, or you aren't a member of it." },
  user: { title: "User not found", body: "There's no user at this address, or the link is wrong." },
  object: { title: "Not found", body: "It may have been deleted, or the link is wrong." },
  /** A signed-in user without the platform role on an admin page (no admin data is requested). */
  forbidden: { title: "No access", body: "The admin portal is for platform admins and auditors." },
};

export type NotFoundWhat = keyof typeof titles;

export function NotFoundState({ what = "page" }: { what?: NotFoundWhat }) {
  const { supportUrl } = useInstance();
  const { capabilities } = useCurrentUser();
  const inAdmin = useRouterState({ select: (st) => st.location.pathname === "/admin" || st.location.pathname.startsWith("/admin/") });
  // An unknown admin address takes admins back to the admin portal, not the workspace.
  const adminHome = inAdmin && what !== "forbidden" && (capabilities.platformAdmin || capabilities.platformAuditor);
  const t = titles[what];
  useCrumbTail(t.title);
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader title={t.title} />
      <Card>
        <EmptyState
          icon={what === "forbidden" ? <ShieldAlert /> : <SearchX />}
          title={t.body}
          description={what === "forbidden" ? "Ask a platform admin if you need access." : adminHome ? "Check the link, or go back to the admin portal." : "Check the link, or go back to your home page."}
          action={
            <>
              {adminHome ? (
                <Button variant="secondary" render={<Link to="/admin" />}>
                  <Shield aria-hidden /> Go to Admin
                </Button>
              ) : (
                <Button variant="secondary" render={<Link to="/" />}>
                  <Home aria-hidden /> Go home
                </Button>
              )}
              {supportUrl && <HelpButton href={supportUrl} />}
            </>
          }
        />
      </Card>
    </Stack>
  );
}
