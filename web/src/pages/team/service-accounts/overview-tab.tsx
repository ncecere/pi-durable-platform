/*
 * A service account's Overview: its role and who answers for it. What its
 * keys reach comes with the agents domain.
 */
import { Badge } from "@/components/ui/badge/badge";
import { Card } from "@/components/ui/card/card";
import { DescriptionList } from "@/components/ui/description-list/description-list";
import { Time } from "@/components/ui/time/time";
import s from "../../shared.module.css";
import { useTeam } from "../common";
import { roleExplanation, roleLabel, type ServiceAccount, type ServiceAccountAbilities, serviceAccountRoleHelp } from "./labels";

type Props = { account: ServiceAccount; abilities: ServiceAccountAbilities };

export function OverviewTab({ account }: Props) {
  const { team } = useTeam();
  return (
    <Card title="Role" description={roleExplanation}>
        <DescriptionList
          dividers
          items={[
            {
              label: "Role",
              value: (
                <span>
                  <Badge tone={account.role === "editor" ? "info" : "neutral"}>{roleLabel(account.role)}</Badge> <span className={s.secondary}>{serviceAccountRoleHelp[account.role]}</span>
                </span>
              ),
            },
            { label: "Responsible", value: `The owners and admins of ${team.name}. The account stays when the person who created it leaves.` },
            { label: "Created", value: <Time value={account.createdAt} /> },
            ...(account.createdByName ? [{ label: "Created by", value: account.createdByName }] : []),
            { label: "Last changed", value: <Time value={account.updatedAt} /> },
          ]}
        />
    </Card>
  );
}
