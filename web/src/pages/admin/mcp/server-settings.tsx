/*
 * An MCP server's Settings: name, description, URL, access mode, who may
 * attach it and the switch (If-Match on its revision), and the write-only
 * headers, sent only when replaced. A new URL or headers make the platform
 * read the tools again. Identity: whether, in proxy mode, requests carry the
 * platform's signed identity token, for which audience, with the person's
 * email or not (docs/mcp-identity.md). Delete is in the Danger zone. A team's own server
 * (in its team, or in Admin) has no access mode (proxy only) and no
 * publishing (its team's alone), and a refused address shows on the URL field.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { invalidateMcp, type McpServer, mcpServerQuery, updateMcpServer } from "@/api/mcp";
import { useRevisionForm } from "@/components/templates/revision-form";
import { DangerAction, DangerZone, SettingsPage, SettingsSection } from "@/components/templates/settings-page";
import { Button } from "@/components/ui/button/button";
import { Field } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { RadioGroup } from "@/components/ui/radio-group/radio-group";
import { Switch } from "@/components/ui/switch/switch";
import { toast } from "@/components/ui/toast/toast";
import s from "../../shared.module.css";
import { HeadersSetting } from "../models/headers-editor";
import { accessLabels, type HeaderRow, headersOf, headersProblem, urlProblem, urlRefusal } from "../models/labels";
import { type McpAccessMode, mcpAccessOptions, type McpVisibility, takenMessage, teamMcpAccessNote, teamOnly, teamsProblem, teamUrlHint } from "./labels";
import { PublishFields } from "./publish-fields";
import { isTeamOwned, type McpCatalogScope } from "./scope";

type Form = {
  name: string;
  description: string;
  url: string;
  accessMode: McpAccessMode;
  enabled: boolean;
  visibility: McpVisibility;
  teams: string[];
  personal: boolean;
  sendIdentity: boolean;
  identityAudience: string;
  identityEmail: boolean;
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
  sendIdentity: m.sendIdentity,
  identityAudience: m.identityAudience ?? "",
  identityEmail: m.identityEmail,
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
  sendIdentity: "Send the identity token",
  identityAudience: "Token audience",
  identityEmail: "Include the person's email",
};
const choices = { accessMode: accessLabels, visibility: { all: "All teams", teams: "Selected teams" } };

type Props = { catalog: McpCatalogScope; server: McpServer; onDelete: () => void };

export function McpSettingsTab({ catalog, server, onDelete }: Props) {
  const { scope, canManage, readOnlyNote } = catalog;
  const teamOwned = isTeamOwned(scope, server);
  const qc = useQueryClient();
  const saved = formOf(server);
  const [form, setForm, revision] = useRevisionForm(saved, server.revision, { labels, choices });
  // Write-only: never loaded, so kept out of the revision form. null: keep the current headers.
  const [headers, setHeaders] = useState<HeaderRow[] | null>(null);
  const [submitted, setSubmitted] = useState(false);
  const problems = {
    name: !form.name.trim() ? "Enter a name." : undefined,
    url: urlProblem(form.url, true, teamOwned),
    teams: teamOwned ? undefined : teamsProblem(form.visibility, form.teams),
    headers: headers ? headersProblem(headers) : undefined,
    identityAudience: /\s/.test(form.identityAudience.trim()) ? "No spaces: an audience is usually a URL." : undefined,
  };
  const invalid = submitted && Object.values(problems).some(Boolean);
  const save = useMutation({
    mutationFn: async () => {
      const common = {
        name: form.name.trim(),
        description: form.description.trim(),
        url: form.url.trim(),
        enabled: form.enabled,
        headers: headers ? headersOf(headers) : undefined,
        sendIdentity: form.sendIdentity,
        identityAudience: form.identityAudience.trim(),
        identityEmail: form.identityEmail,
      };
      // A team's server: no access mode or publishing (the API refuses them).
      return updateMcpServer(
        scope,
        server,
        teamOwned
          ? common
          : { ...common, accessMode: form.accessMode, visibility: form.visibility, teams: form.visibility === "teams" ? form.teams : undefined, personal: form.personal },
      );
    },
    onSuccess: (updated) => {
      qc.setQueryData(mcpServerQuery(scope, updated.id).queryKey, updated);
      void invalidateMcp(qc, scope);
      setHeaders(null);
      setSubmitted(false);
      if (updated.toolsError) toast.warning("Saved, but the tools couldn't be read", updated.toolsError);
      else toast.success("MCP server saved");
    },
  });
  const taken = takenMessage(save.error);
  const refused = urlRefusal(save.error);
  const dirty = JSON.stringify(form) !== JSON.stringify(saved) || headers !== null;
  const shown = (p: string | undefined) => (submitted ? p : undefined);
  const set = (patch: Partial<Form>) => {
    if (taken || refused) save.reset();
    setForm((f) => ({ ...f, ...patch }));
  };
  return (
    <SettingsPage
      revision={revision}
      dirty={dirty}
      canEdit={canManage}
      readOnlyNote={readOnlyNote}
      saving={save.isPending}
      error={taken || refused ? undefined : save.error}
      message={invalid || taken || refused ? "Not saved: fix the highlighted field" : undefined}
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
      <SettingsSection title="General" description={`Its tools are named mcp__${server.slug}__<tool>.`}>
        <Field label="Name" error={shown(problems.name) ?? taken}>
          <Input maxLength={100} disabled={!canManage} value={form.name} onValueChange={(v) => set({ name: v })} />
        </Field>
        <Field label="Slug" description="Names its tools. It can't be changed.">
          <Input disabled readOnly spellCheck={false} value={server.slug} />
        </Field>
        <Field label="Description" labelHint="Optional">
          <Textarea maxLength={2000} rows={2} disabled={!canManage} value={form.description} onChange={(e) => set({ description: e.target.value })} />
        </Field>
        <Field
          label="URL"
          description={`Its Streamable HTTP endpoint. A new URL makes the platform read the tools again.${teamOwned ? ` ${teamUrlHint}` : ""}`}
          error={shown(problems.url) ?? refused}
        >
          <Input maxLength={2000} spellCheck={false} disabled={!canManage} value={form.url} onValueChange={(v) => set({ url: v })} />
        </Field>
        {teamOwned ? (
          <p className={s.note}>{teamMcpAccessNote}</p>
        ) : (
          <RadioGroup legend="How agents reach it" disabled={!canManage} value={form.accessMode} onValueChange={(v) => set({ accessMode: v })} options={mcpAccessOptions} />
        )}
        <Switch
          label="Enabled"
          description="Turned off, templates can't pick it, and agents don't get its tools until it's turned on again."
          disabled={!canManage}
          checked={form.enabled}
          onCheckedChange={(v) => set({ enabled: v })}
        />
      </SettingsSection>
      {teamOwned ? (
        <SettingsSection title="Who may use it">
          <p className={s.note}>
            {teamOnly(server)}: only the team&apos;s templates can give its tools to their agents. It can&apos;t be published to other teams, and templates using it
            can&apos;t go to the catalog.
          </p>
        </SettingsSection>
      ) : (
        <SettingsSection title="Who may use it" description="Teams it's published to can give its tools to their templates' agents; personal workspaces, to their agents.">
          <PublishFields
            visibility={form.visibility}
            teams={form.teams}
            personal={form.personal}
            currentTeams={server.teams}
            disabled={!canManage}
            error={shown(problems.teams)}
            onChange={set}
          />
        </SettingsSection>
      )}
      <SettingsSection title="Headers" description="Sent with every request to the server, for example Authorization. Stored encrypted and never shown, here or in the API.">
        <HeadersSetting
          has={server.hasHeaders}
          rows={headers}
          onChange={setHeaders}
          error={shown(problems.headers)}
          isAdmin={canManage}
          hint="Sent with every request to the server. Saving reads the tools again."
        />
      </SettingsSection>
      <SettingsSection
        title="Identity"
        description="Through the platform (proxy), every request carries a short-lived token the platform signs (MCP-Gateway-Identity): the agent, its team, the thread's visibility and, in a personal workspace or a private thread, the person. Direct access never does."
      >
        <Switch
          label="Send the identity token"
          description="Turn it off for servers outside your organisation."
          disabled={!canManage}
          checked={form.sendIdentity}
          onCheckedChange={(v) => set({ sendIdentity: v })}
        />
        <Field label="Token audience" labelHint="Optional" description="What the token's aud says. Empty: the server's URL." error={shown(problems.identityAudience)}>
          <Input
            maxLength={2048}
            spellCheck={false}
            disabled={!canManage || !form.sendIdentity}
            placeholder={form.url}
            value={form.identityAudience}
            onValueChange={(v) => set({ identityAudience: v })}
          />
        </Field>
        <Switch
          label="Include the person's email"
          description="Off: the server finds the person by their sign-in identity (issuer and subject)."
          disabled={!canManage || !form.sendIdentity}
          checked={form.identityEmail}
          onCheckedChange={(v) => set({ identityEmail: v })}
        />
      </SettingsSection>
      {canManage && (
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
