/* Team settings → Members: the member list, open invites (only when there are some) and "Add member". */
import { UserPlus } from "lucide-react";
import { useState } from "react";
import { AddMemberDialog, InviteList, MemberList } from "../../../components/members";
import { useIntent } from "../../../lib/intents";
import { useCurrentUser } from "../../../session";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { ReadOnlyNotice } from "../access";
import { useTeam } from "../common";

export function MembersTab() {
  const { slug, role, archived, isManager } = useTeam();
  const me = useCurrentUser();
  const [adding, setAdding] = useState(false);
  const canAdd = isManager && !archived;
  // Platform admins revoke any open invite (an owner invite sent from Admin → Teams); auditors only see them.
  const staffInvites = !role && (me.capabilities.platformAdmin || me.capabilities.platformAuditor);
  const inviteRole = archived ? undefined : (role ?? (me.capabilities.platformAdmin ? "owner" : undefined));
  useIntent("add-member", () => canAdd && setAdding(true));
  return (
    <>
      <ReadOnlyNotice what="the team's members" managers change="add, change and remove them" />
      <Card
        title="Members"
        actions={
          canAdd && (
            <Button size="sm" onClick={() => setAdding(true)}>
              <UserPlus aria-hidden /> Add member
            </Button>
          )
        }
        flush
      >
        <MemberList team={slug} myRole={archived ? undefined : role} myUserId={me.user.id} />
      </Card>
      {(isManager || staffInvites) && (
        <InviteList team={slug} myRole={inviteRole} />
      )}
      <AddMemberDialog team={slug} myRole={role} open={adding} onOpenChange={setAdding} />
    </>
  );
}
