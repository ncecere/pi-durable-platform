/* Admin team › Settings: name and description in one form, and Archive in the Danger zone. */
import { adminOnly } from "@/lib/terms";
import type { Schemas } from "@/api/client";
import { DangerAction, DangerZone, SettingsPage, SettingsSection } from "@/components/templates/settings-page";
import { useRevisionForm } from "@/components/templates/revision-form";
import { Button } from "@/components/ui/button/button";
import { CopyField } from "@/components/ui/copy-field/copy-field";
import { Field } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import type { useTeamUpdate } from "./team";

type Form = { name: string; description: string };
const formOf = (t: Schemas["Team"]): Form => ({ name: t.name, description: t.description });
const labels = { name: "Name", description: "Description" };
const nameError = (name: string) => (name.trim() === "" ? "Enter a name." : name.trim().length > 100 ? "Use at most 100 characters." : undefined);

type Props = { team: Schemas["Team"]; isAdmin: boolean; onArchive: () => void; status: ReturnType<typeof useTeamUpdate> };

export function TeamSettingsTab({ team, isAdmin, onArchive, status }: Props) {
  const saved = formOf(team);
  // Edits survive a change made elsewhere (another tab or admin): SettingsPage asks whose to keep.
  const [form, setForm, revision] = useRevisionForm(saved, team.revision, { labels });
  const dirty = form.name.trim() !== saved.name || form.description !== saved.description;
  const invalid = nameError(form.name);
  const archived = team.status === "archived";
  return (
    <SettingsPage
      revision={revision}
      dirty={dirty}
      canEdit={isAdmin}
      readOnlyNote={adminOnly}
      saving={status.isPending && !("status" in (status.variables ?? {}))}
      error={status.error}
      saveLabel="Save settings"
      onSave={() => status.mutate({ name: form.name.trim(), description: form.description })}
      onDiscard={() => setForm(saved)}
      saveDisabled={Boolean(invalid)}
      message={invalid ? "Not saved: fix the highlighted field" : undefined}
    >
      <SettingsSection title="General" description="The team's name and description, shown to its members and in the team switcher.">
        <Field label="Name" error={invalid}>
          <Input required maxLength={100} disabled={!isAdmin} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
        </Field>
        <Field label="Description" labelHint="Optional">
          <Textarea maxLength={2000} disabled={!isAdmin} value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} />
        </Field>
        <CopyField label="Slug" name="team slug" value={team.slug} description="Used in the team's addresses. It can't be changed, so links keep working." />
      </SettingsSection>
      {isAdmin && (
        <DangerZone>
          {archived ? (
            <DangerAction
              title="Unarchive this team"
              description="Members can change the team again."
              action={
                <Button variant="secondary" loading={status.isPending} onClick={() => status.mutate({ status: "active" })}>
                  Unarchive team
                </Button>
              }
            />
          ) : (
            <DangerAction
              title="Archive this team"
              description="The team becomes read-only for its members. You can unarchive it later."
              action={
                <Button variant="danger" onClick={onArchive}>
                  Archive team
                </Button>
              }
            />
          )}
        </DangerZone>
      )}
    </SettingsPage>
  );
}
