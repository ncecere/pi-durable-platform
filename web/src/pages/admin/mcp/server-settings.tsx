/*
 * An MCP server's Settings: name, description, URL, access mode, who may
 * attach it and the switch (If-Match on its revision), and the write-only
 * headers, sent only when replaced. A new URL or headers make the platform
 * read the tools again. Delete is in the Danger zone.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, ApiError, ifMatch, unwrap } from "@/api/client";
import { invalidateMcp, type McpServer, mcpServerQuery } from "@/api/mcp";
import { useRevisionForm } from "@/components/templates/revision-form";
import { DangerAction, DangerZone, SettingsPage, SettingsSection } from "@/components/templates/settings-page";
import { Button } from "@/components/ui/button/button";
import { Field } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { RadioGroup } from "@/components/ui/radio-group/radio-group";
import { Switch } from "@/components/ui/switch/switch";
import { toast } from "@/components/ui/toast/toast";
import { adminOnly } from "@/lib/terms";
import { HeadersSetting } from "../models/headers-editor";
import { accessLabels, type HeaderRow, headersOf, headersProblem, urlProblem } from "../models/labels";
import { type McpAccessMode, mcpAccessOptions, type McpVisibility, teamsProblem } from "./labels";
import { PublishFields } from "./publish-fields";

type Form = {
  name: string;
  description: string;
  url: string;
  accessMode: McpAccessMode;
  enabled: boolean;
  visibility: McpVisibility;
  teams: string[];
  personal: boolean;
};
const formOf = (m: McpServer): Form => ({
  name: m.name,
  description: m.description,
  url: m.url,
  accessMode: m.accessMode,
  enabled: m.enabled,
  visibility: m.visibility,
  teams: (m.teams ?? []).map((t) => t.slug),
  personal: m.personal,
});
const labels = {
  name: "Name",
  description: "Description",
  url: "URL",
  accessMode: "Access",
  enabled: "Enabled",
  visibility: "Published to",
  teams: "Teams",
  personal: "Personal workspaces",
};
const choices = { accessMode: accessLabels, visibility: { all: "All teams", teams: "Selected teams" } };

type Props = { server: McpServer; isAdmin: boolean; onDelete: () => void };

export function McpSettingsTab({ server, isAdmin, onDelete }: Props) {
  const qc = useQueryClient();
  const saved = formOf(server);
  const [form, setForm, revision] = useRevisionForm(saved, server.revision, { labels, choices });
  // Write-only: never loaded, so kept out of the revision form. null: keep the current headers.
  const [headers, setHeaders] = useState<HeaderRow[] | null>(null);
  const [submitted, setSubmitted] = useState(false);
  const problems = {
    name: !form.name.trim() ? "Enter a name." : undefined,
    url: urlProblem(form.url, true),
    teams: teamsProblem(form.visibility, form.teams),
    headers: headers ? headersProblem(headers) : undefined,
  };
  const invalid = submitted && Object.values(problems).some(Boolean);
  const save = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.PATCH("/v1/admin/mcp-servers/{serverId}", {
          params: { path: { serverId: server.id }, header: ifMatch(server.revision) },
          body: {
            name: form.name.trim(),
            description: form.description.trim(),
            url: form.url.trim(),
            accessMode: form.accessMode,
            enabled: form.enabled,
            visibility: form.visibility,
            teams: form.visibility === "teams" ? form.teams : undefined,
            personal: form.personal,
            headers: headers ? headersOf(headers) : undefined,
          },
        }),
      ),
    onSuccess: (updated) => {
      qc.setQueryData(mcpServerQuery(updated.id).queryKey, updated);
      void invalidateMcp(qc);
      setHeaders(null);
      setSubmitted(false);
      if (updated.toolsError) toast.warning("Saved, but the tools couldn't be read", updated.toolsError);
      else toast.success("MCP server saved");
    },
  });
  const taken = save.error instanceof ApiError && save.error.code === "mcp_server_name_taken";
  const dirty = JSON.stringify(form) !== JSON.stringify(saved) || headers !== null;
  const shown = (p: string | undefined) => (submitted ? p : undefined);
  const set = (patch: Partial<Form>) => setForm((f) => ({ ...f, ...patch }));
  return (
    <SettingsPage
      revision={revision}
      dirty={dirty}
      canEdit={isAdmin}
      readOnlyNote={adminOnly}
      saving={save.isPending}
      error={taken ? undefined : save.error}
      message={invalid || taken ? "Not saved: fix the highlighted field" : undefined}
      onSave={() => {
        setSubmitted(true);
        if (Object.values(problems).some(Boolean)) return;
        save.mutate();
      }}
      onDiscard={() => {
        setForm(saved);
        setHeaders(null);
        setSubmitted(false);
        save.reset();
      }}
    >
      <SettingsSection title="General" description={`Its tools are named mcp__${server.slug}__<tool>. The slug can't be changed.`}>
        <Field label="Name" error={shown(problems.name) ?? (taken ? "Another MCP server has this name." : undefined)}>
          <Input maxLength={100} disabled={!isAdmin} value={form.name} onValueChange={(v) => set({ name: v })} />
        </Field>
        <Field label="Description" labelHint="Optional">
          <Textarea maxLength={2000} rows={2} disabled={!isAdmin} value={form.description} onChange={(e) => set({ description: e.target.value })} />
        </Field>
        <Field label="URL" description="Its Streamable HTTP endpoint. A new URL makes the platform read the tools again." error={shown(problems.url)}>
          <Input maxLength={2000} spellCheck={false} disabled={!isAdmin} value={form.url} onValueChange={(v) => set({ url: v })} />
        </Field>
        <RadioGroup legend="How agents reach it" disabled={!isAdmin} value={form.accessMode} onValueChange={(v) => set({ accessMode: v })} options={mcpAccessOptions} />
        <Switch
          label="Enabled"
          description="Turned off, templates can't pick it, and agents don't get its tools until it's turned on again."
          disabled={!isAdmin}
          checked={form.enabled}
          onCheckedChange={(v) => set({ enabled: v })}
        />
      </SettingsSection>
      <SettingsSection title="Who may use it" description="Teams it's published to can give its tools to their templates' agents; personal workspaces, to their agents.">
        <PublishFields
          visibility={form.visibility}
          teams={form.teams}
          personal={form.personal}
          currentTeams={server.teams}
          disabled={!isAdmin}
          error={shown(problems.teams)}
          onChange={set}
        />
      </SettingsSection>
      <SettingsSection title="Headers" description="Sent with every request to the server, for example Authorization. Stored encrypted and never shown, here or in the API.">
        <HeadersSetting
          has={server.hasHeaders}
          rows={headers}
          onChange={setHeaders}
          error={shown(problems.headers)}
          isAdmin={isAdmin}
          hint="Sent with every request to the server. Saving reads the tools again."
        />
      </SettingsSection>
      {isAdmin && (
        <DangerZone>
          <DangerAction
            title="Delete this MCP server"
            description="Deletes it and its headers. Refused while template versions use it: turn it off instead."
            action={
              <Button variant="danger" onClick={onDelete}>
                Delete MCP server
              </Button>
            }
          />
        </DangerZone>
      )}
    </SettingsPage>
  );
}
