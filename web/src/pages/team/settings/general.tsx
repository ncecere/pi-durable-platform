/*
 * Team settings › General: the team's details on the SettingsPage template,
 * and the Danger zone. Platform admins rename teams, so the form is editable
 * for them (one save bar, the unsaved-changes guard; PATCH
 * /v1/admin/teams/{team}) and read-only for everyone else, who are told whom
 * to ask. Danger zone: members leave the team (not the only owner); platform
 * admins archive or unarchive it.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, ifMatch, unwrap } from "../../../api/client";
import { membersQuery, teamQuery } from "../../../api/queries";
import { ConfirmMutationDialog } from "../../../components/confirm-dialog";
import { leaveBlockedReason } from "../../../components/members";
import { RoleBadge } from "../../../components/role-badge";
import { useRevisionForm } from "../../../components/templates/revision-form";
import { DangerAction, DangerZone, SettingsPage, SettingsSection } from "../../../components/templates/settings-page";
import { adminOnly, lifecycleLabels } from "../../../lib/terms";
import { useCurrentUser } from "../../../session";
import { Button } from "@/components/ui/button/button";
import { CopyField } from "@/components/ui/copy-field/copy-field";
import { DescriptionList } from "@/components/ui/description-list/description-list";
import { Field } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { Time } from "@/components/ui/time/time";
import { toast } from "@/components/ui/toast/toast";
import { type Team, useTeam } from "../common";

type Form = { name: string; description: string };
const formOf = (t: Team): Form => ({ name: t.name, description: t.description });
const nameError = (name: string) => (name.trim().length < 1 ? "Enter a name." : name.trim().length > 100 ? "Use at most 100 characters." : undefined);

export function GeneralTab() {
  const { slug, team, role, archived } = useTeam();
  const me = useCurrentUser();
  const qc = useQueryClient();
  const isPlatformAdmin = me.capabilities.platformAdmin;
  // Edits survive a change made elsewhere; SettingsPage asks whose to keep.
  const [form, setForm, revision] = useRevisionForm<Form>(formOf(team), team.revision);
  const save = useMutation({
    mutationFn: async (body: { name?: string; description?: string; status?: "active" | "archived" }) =>
      unwrap(await api.PATCH("/v1/admin/teams/{team}", { params: { path: { team: slug }, header: ifMatch(team.revision) }, body })),
    onSuccess: (_, body) => {
      toast.success(body.status === "archived" ? "Team archived" : body.status === "active" ? "Team unarchived" : "Team saved");
      void qc.invalidateQueries({ queryKey: teamQuery(slug).queryKey });
      void qc.invalidateQueries({ queryKey: ["me"] });
      void qc.invalidateQueries({ queryKey: ["admin"] });
    },
  });
  const dirty = form.name.trim() !== team.name || form.description !== team.description;
  const invalid = nameError(form.name);
  const address = `${globalThis.location?.origin ?? ""}/teams/${slug}`;

  return (
    <SettingsPage
      revision={revision}
      canEdit={isPlatformAdmin}
      // One notice for people who can't rename the team (the section said it a second time).
      readOnlyNote={role ? "Platform admins rename teams. Ask one if something here needs to change." : adminOnly}
      dirty={dirty}
      saving={save.isPending && !("status" in (save.variables ?? {}))}
      error={save.error}
      onSave={() => save.mutate({ name: form.name.trim(), description: form.description })}
      onDiscard={() => setForm(formOf(team))}
      message={invalid ? "Not saved: fix the highlighted field" : undefined}
      saveDisabled={Boolean(invalid)}
    >
      <SettingsSection
        title="General"
        description="The team's name and description, shown to its members and in the team switcher."
      >
        {isPlatformAdmin ? (
          <>
            <Field label="Name" error={invalid}>
              <Input required maxLength={100} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
            </Field>
            <Field label="Description" labelHint="Optional">
              <Textarea maxLength={2000} value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} />
            </Field>
          </>
        ) : (
          <DescriptionList
            dividers
            items={[
              { label: "Name", value: team.name },
              { label: "Description", value: team.description || "No description" },
            ]}
          />
        )}
        <CopyField label="Address" name="team address" value={address} description="The team's pages. Its slug can't change, so links keep working." />
      </SettingsSection>
      <SettingsSection title="Status and access">
        <DescriptionList
          dividers
          items={[
            { label: "Status", value: archived ? `${lifecycleLabels.archived}: read-only` : lifecycleLabels.active },
            { label: "Your role", value: role ? <RoleBadge role={role} /> : "Not a member (platform staff)" },
            { label: "Created", value: <Time value={team.createdAt} format="date" /> },
          ]}
        />
      </SettingsSection>
      {(role || isPlatformAdmin) && (
        <DangerZone>
          {role && <LeaveTeam slug={slug} myUserId={me.user.id} />}
          {isPlatformAdmin && <ArchiveTeam archived={archived} name={team.name} save={save} />}
        </DangerZone>
      )}
    </SettingsPage>
  );
}

type Save = { mutate: (body: { status: "active" | "archived" }, opts?: { onSuccess?: () => void }) => void; isPending: boolean; error: unknown; reset: () => void };

function ArchiveTeam({ archived, name, save }: { archived: boolean; name: string; save: Save }) {
  const [confirming, setConfirming] = useState<true | null>(null);
  const next = archived ? "active" : "archived";
  return (
    <>
      <DangerAction
        title={archived ? "Unarchive this team" : "Archive this team"}
        description={archived ? "Members can change the team again." : "Everything becomes read-only. Members keep read access. You can unarchive it later."}
        action={
          <Button variant={archived ? "secondary" : "danger"} onClick={() => setConfirming(true)}>
            {archived ? "Unarchive team" : "Archive team"}
          </Button>
        }
      />
      <ConfirmMutationDialog
        target={confirming}
        onClose={() => setConfirming(null)}
        mutation={save}
        onConfirm={() => save.mutate({ status: next }, { onSuccess: () => setConfirming(null) })}
        title={archived ? `Unarchive ${name}?` : `Archive ${name}?`}
        description={archived ? "The team becomes editable again." : "The team becomes read-only for everyone until a platform admin unarchives it."}
        confirmLabel={archived ? "Unarchive team" : "Archive team"}
      />
    </>
  );
}

function LeaveTeam({ slug, myUserId }: { slug: string; myUserId: string }) {
  const { role } = useTeam();
  const qc = useQueryClient();
  const [confirming, setConfirming] = useState<true | null>(null);
  const members = useQuery(membersQuery(slug));
  const leave = useMutation({
    mutationFn: async () => unwrap(await api.DELETE("/v1/teams/{team}/members/{userId}", { params: { path: { team: slug, userId: myUserId } } })),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["me"] });
      window.location.assign("/");
    },
  });
  const blocked = leaveBlockedReason(members.data, role);
  return (
    <>
      <DangerAction
        title="Leave this team"
        description="You lose access to the team. An admin or owner can add you again."
        disabledReason={blocked}
        action={
          <Button variant="danger" disabled={!!blocked || members.isLoading} onClick={() => setConfirming(true)}>
            Leave team
          </Button>
        }
      />
      <ConfirmMutationDialog
        target={confirming}
        onClose={() => setConfirming(null)}
        mutation={leave}
        onConfirm={() => leave.mutate()}
        title="Leave this team?"
        description="You will lose access to this team."
        confirmLabel="Leave team"
      />
    </>
  );
}
