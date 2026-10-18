/*
 * Team › Models › one of the team's own providers
 * (/teams/$team/models/providers/$providerId): Admin's provider page in the
 * team's scope. Proxy only, its models for the team alone (no access mode or
 * publishing); the team's owners and admins change it, its other members read.
 */
import { useParams } from "@tanstack/react-router";
import { teamScope } from "@/api/models";
import { ProviderPage } from "../../admin/models/provider";
import { useTeam } from "../common";
import { managesProviders, readOnlyNote } from "./access";

export function TeamModelProviderPage() {
  const { providerId } = useParams({ from: "/app/teams/$team/models/providers/$providerId" });
  const ctx = useTeam();
  return <ProviderPage providerId={providerId} catalog={{ scope: teamScope(ctx.slug), canManage: managesProviders(ctx), readOnlyNote: readOnlyNote(ctx) }} />;
}
