/*
 * Add MCP server (platform admins): its name and tool-name slug (prefilled
 * from the name), URL, access mode, who may attach it and optional headers.
 * The platform reads its tools right away; an unreachable server is still
 * added, with the reason on its page.
 */
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { api, ApiError, errorMessage, unwrap } from "@/api/client";
import { invalidateMcp, mcpSlugFrom, mcpSlugPattern } from "@/api/mcp";
import { FormDialog } from "@/components/form-dialog";
import { Disclosure } from "@/components/ui/disclosure/disclosure";
import { Field, Fieldset } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { RadioGroup } from "@/components/ui/radio-group/radio-group";
import { toast } from "@/components/ui/toast/toast";
import { HeadersEditor } from "../models/headers-editor";
import { type HeaderRow, headersOf, headersProblem, urlProblem } from "../models/labels";
import { type McpAccessMode, mcpAccessOptions, type McpVisibility, slugHint, teamsProblem } from "./labels";
import { PublishFields } from "./publish-fields";

export function AddMcpServerDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  // undefined: follow the name until the person types a slug.
  const [typedSlug, setTypedSlug] = useState<string | undefined>(undefined);
  const slug = typedSlug ?? mcpSlugFrom(name);
  const [description, setDescription] = useState("");
  const [url, setUrl] = useState("");
  const [accessMode, setAccessMode] = useState<McpAccessMode>("proxy");
  const [visibility, setVisibility] = useState<McpVisibility>("all");
  const [teams, setTeams] = useState<string[]>([]);
  const [personal, setPersonal] = useState(true);
  const [headers, setHeaders] = useState<HeaderRow[]>([]);
  const [submitted, setSubmitted] = useState(false);
  const [taken, setTaken] = useState(false);
  const [busy, setBusy] = useState(false);
  const problems = {
    name: !name.trim() ? "Enter a name." : undefined,
    slug: !mcpSlugPattern.test(slug) ? "Use 1–32 lower-case letters, digits or underscores, starting and ending with a letter or digit." : undefined,
    url: urlProblem(url, true),
    teams: teamsProblem(visibility, teams),
    headers: headersProblem(headers),
  };
  const shown = (p: string | undefined) => (submitted ? p : undefined);
  const submit = async () => {
    setSubmitted(true);
    if (Object.values(problems).some(Boolean)) return;
    setBusy(true);
    try {
      const extra = headersOf(headers);
      const created = unwrap(
        await api.POST("/v1/admin/mcp-servers", {
          body: {
            name: name.trim(),
            slug,
            description: description.trim() || undefined,
            url: url.trim(),
            accessMode,
            visibility,
            teams: visibility === "teams" ? teams : undefined,
            personal,
            headers: Object.keys(extra).length ? extra : undefined,
          },
        }),
      );
      await invalidateMcp(qc);
      if (created.toolsError) toast.warning(`${created.name} was added, but its tools couldn't be read`, created.toolsError);
      else toast.success(`${created.name} was added`, `It offers ${created.tools.length === 1 ? "1 tool" : `${created.tools.length} tools`}.`);
      onClose();
      void navigate({ to: "/admin/mcp-servers/$serverId", params: { serverId: created.id } });
    } catch (err) {
      if (err instanceof ApiError && err.code === "mcp_server_name_taken") setTaken(true);
      else toast.error("Couldn't add the MCP server", errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const takenMessage = taken ? "Another MCP server has this name or slug." : undefined;
  return (
    <FormDialog
      title="Add MCP server"
      description="The platform reads its tools when it's added. Header values are stored encrypted and never shown again."
      size="lg"
      onClose={onClose}
      onSubmit={() => void submit()}
      submitLabel="Add MCP server"
      busy={busy}
    >
      <Field label="Name" description="How admins and teams see it, for example “GitHub” or “Ticketing”." error={shown(problems.name) ?? takenMessage}>
        <Input
          value={name}
          maxLength={100}
          autoComplete="off"
          onValueChange={(v) => {
            setName(v);
            setTaken(false);
          }}
        />
      </Field>
      <Field label="Slug" description={slugHint(slug)} error={shown(problems.slug)}>
        <Input
          value={slug}
          maxLength={32}
          spellCheck={false}
          autoComplete="off"
          onValueChange={(v) => {
            setTypedSlug(v.toLowerCase());
            setTaken(false);
          }}
        />
      </Field>
      <Field label="Description" labelHint="Optional">
        <Textarea value={description} maxLength={2000} rows={2} onChange={(e) => setDescription(e.target.value)} />
      </Field>
      <Field label="URL" description="The server's Streamable HTTP endpoint, for example https://mcp.example.com/mcp." error={shown(problems.url)}>
        <Input value={url} spellCheck={false} autoComplete="off" maxLength={2000} placeholder="https://" onValueChange={setUrl} />
      </Field>
      <RadioGroup legend="How agents reach it" value={accessMode} onValueChange={setAccessMode} options={mcpAccessOptions} />
      <PublishFields
        visibility={visibility}
        teams={teams}
        personal={personal}
        error={shown(problems.teams)}
        onChange={(next) => {
          if (next.visibility) setVisibility(next.visibility);
          if (next.teams) setTeams(next.teams);
          if (next.personal !== undefined) setPersonal(next.personal);
        }}
      />
      <Disclosure title="Advanced" summary={headers.length ? `${headers.length} ${headers.length === 1 ? "header" : "headers"}` : undefined}>
        <Fieldset legend="Headers" description="Optional. Sent with every request to the server, for example Authorization: Bearer …. Stored encrypted.">
          <HeadersEditor rows={headers} onChange={setHeaders} error={shown(problems.headers)} />
        </Fieldset>
      </Disclosure>
    </FormDialog>
  );
}
