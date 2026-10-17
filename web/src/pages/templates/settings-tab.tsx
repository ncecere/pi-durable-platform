/*
 * A template's Settings: name, description and archived (If-Match on its
 * revision). Platform admins change platform templates; a team's editors and
 * above change its templates. Instructions change by adding a version.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, ifMatch, unwrap } from "../../api/client";
import { useRevisionForm } from "../../components/templates/revision-form";
import {
  SettingsPage,
  SettingsSection,
} from "../../components/templates/settings-page";
import { Field } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { Switch } from "@/components/ui/switch/switch";
import { toast } from "@/components/ui/toast/toast";
import {
  type AgentTemplate,
  storeTemplate,
  type TemplateAbilities,
} from "./labels";

type Form = { name: string; description: string; archived: boolean };
const formOf = (t: AgentTemplate): Form => ({
  name: t.name,
  description: t.description,
  archived: t.archived,
});
const labels = {
  name: "Name",
  description: "Description",
  archived: "Archived",
};

export function TemplateSettingsTab({
  template,
  abilities,
}: {
  template: AgentTemplate;
  abilities: TemplateAbilities;
}) {
  const qc = useQueryClient();
  const saved = formOf(template);
  const [form, setForm, revision] = useRevisionForm(saved, template.revision, {
    labels,
  });
  const [submitted, setSubmitted] = useState(false);
  const save = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.PATCH("/v1/agent-templates/{templateId}", {
          params: {
            path: { templateId: template.id },
            header: ifMatch(template.revision),
          },
          body: {
            name: form.name.trim(),
            description: form.description,
            archived: form.archived,
          },
        }),
      ),
    onSuccess: (t) => {
      setSubmitted(false);
      storeTemplate(qc, t);
      toast.success(
        "Template saved",
        t.archived !== template.archived
          ? t.archived
            ? "It takes no new deployments."
            : "It can be deployed again."
          : undefined,
      );
    },
  });
  const nameError =
    submitted && !form.name.trim() ? "Enter a name." : undefined;
  return (
    <SettingsPage
      revision={revision}
      canEdit={abilities.canEdit}
      readOnlyNote={
        template.teamSlug
          ? undefined
          : "Platform admins change platform templates."
      }
      dirty={JSON.stringify(form) !== JSON.stringify(saved)}
      saving={save.isPending}
      error={save.error}
      message={nameError ? "Not saved: fix the highlighted field" : undefined}
      onSave={() => {
        setSubmitted(true);
        if (!form.name.trim()) return;
        save.mutate();
      }}
      onDiscard={() => {
        setForm(saved);
        setSubmitted(false);
        save.reset();
      }}
    >
      <SettingsSection
        title="General"
        description="How the template shows in lists. Its instructions change by adding a version."
      >
        <Field label="Name" error={nameError}>
          <Input
            maxLength={100}
            disabled={!abilities.canEdit}
            value={form.name}
            onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
          />
        </Field>
        <Field label="Description" labelHint="Optional">
          <Textarea
            maxLength={2000}
            disabled={!abilities.canEdit}
            value={form.description}
            onChange={(e) =>
              setForm((f) => ({ ...f, description: e.target.value }))
            }
          />
        </Field>
        <Switch
          label="Archived"
          description="An archived template takes no new deployments. Agents deployed from it keep their version and keep working."
          disabled={!abilities.canEdit}
          checked={form.archived}
          onCheckedChange={(v) => setForm((f) => ({ ...f, archived: v }))}
        />
      </SettingsSection>
    </SettingsPage>
  );
}
