/*
 * Add MCP server: its name and tool-name slug (prefilled from the name), URL,
 * access mode, who may attach it and optional headers. Platform admins add
 * the platform's; a team's owners and admins add the team's own, which are
 * proxy-only and for the team alone (no access mode, no publishing) and need
 * an https URL on an address teams may use (a refusal shows on the URL
 * field). The platform reads its tools right away; an unreachable server is
 * still added, with the reason on its page.
 */
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { errorMessage } from "@/api/client";
import { createMcpServer, invalidateMcp, type McpScope, mcpSlugFrom, mcpSlugPattern } from "@/api/mcp";
import { FormDialog } from "@/components/form-dialog";
import { Disclosure } from "@/components/ui/disclosure/disclosure";
import { Field, Fieldset } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { RadioGroup } from "@/components/ui/radio-group/radio-group";
import { toast } from "@/components/ui/toast/toast";
import { HeadersEditor } from "../models/headers-editor";
import { type HeaderRow, headersOf, headersProblem, urlProblem, urlRefusal } from "../models/labels";
import s from "../../shared.module.css";
import { type McpAccessMode, mcpAccessOptions, type McpVisibility, slugHint, takenMessage, teamMcpAccessNote, teamsProblem, teamUrlHint } from "./labels";
import { PublishFields } from "./publish-fields";
import { useMcpNavigate } from "./scope";

export function AddMcpServerDialog({ scope, onClose }: { scope: McpScope; onClose: () => void }) {
  const qc = useQueryClient();
  const go = useMcpNavigate(scope);
  const forTeam = scope.kind === "team";
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
  // The server's refusals: the name or slug taken (its message), the URL's address.
  const [taken, setTaken] = useState<string | undefined>(undefined);
  const [refused, setRefused] = useState<string | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const problems = {
    name: !name.trim() ? "Enter a name." : undefined,
    slug: !mcpSlugPattern.test(slug) ? "Use 1–32 lower-case letters, digits or underscores, starting and ending with a letter or digit." : undefined,
    url: urlProblem(url, true, forTeam),
    teams: forTeam ? undefined : teamsProblem(visibility, teams),
    headers: headersProblem(headers),
  };
  const shown = (p: string | undefined) => (submitted ? p : undefined);
  const submit = async () => {
    setSubmitted(true);
    if (Object.values(problems).some(Boolean)) return;
    setBusy(true);
    try {
      const extra = headersOf(headers);
      const common = {
        name: name.trim(),
        slug,
        description: description.trim() || undefined,
        url: url.trim(),
        headers: Object.keys(extra).length ? extra : undefined,
      };
      // A team's servers are proxy-only and its own: no access mode or publishing (the API refuses them).
      const created = await createMcpServer(
        scope,
        forTeam ? common : { ...common, accessMode, visibility, teams: visibility === "teams" ? teams : undefined, personal },
      );
      await invalidateMcp(qc, scope);
      if (created.toolsError) toast.warning(`${created.name} was added, but its tools couldn't be read`, created.toolsError);
      else toast.success(`${created.name} was added`, `It offers ${created.tools.length === 1 ? "1 tool" : `${created.tools.length} tools`}.`);
      onClose();
      void go.server(created.id);
    } catch (err) {
      if (takenMessage(err)) setTaken(takenMessage(err));
      else if (urlRefusal(err)) setRefused(urlRefusal(err));
      else toast.error("Couldn't add the MCP server", errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
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
      <Field label="Name" description={`How ${forTeam ? "the team sees" : "admins and teams see"} it, for example “GitHub” or “Ticketing”.`} error={shown(problems.name) ?? taken}>
        <Input
          value={name}
          maxLength={100}
          autoComplete="off"
          onValueChange={(v) => {
            setName(v);
            setTaken(undefined);
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
            setTaken(undefined);
          }}
        />
      </Field>
      <Field label="Description" labelHint="Optional">
        <Textarea value={description} maxLength={2000} rows={2} onChange={(e) => setDescription(e.target.value)} />
      </Field>
      <Field
        label="URL"
        description={`The server's Streamable HTTP endpoint, for example https://mcp.example.com/mcp.${forTeam ? ` ${teamUrlHint}` : ""}`}
        error={shown(problems.url) ?? refused}
      >
        <Input
          value={url}
          spellCheck={false}
          autoComplete="off"
          maxLength={2000}
          placeholder="https://"
          onValueChange={(v) => {
            setUrl(v);
            setRefused(undefined);
          }}
        />
      </Field>
      {forTeam ? (
        <p className={s.note}>{teamMcpAccessNote} Only this team&apos;s templates can give its tools to their agents.</p>
      ) : (
        <>
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
        </>
      )}
      <Disclosure title="Advanced" summary={headers.length ? `${headers.length} ${headers.length === 1 ? "header" : "headers"}` : undefined}>
        <Fieldset legend="Headers" description="Optional. Sent with every request to the server, for example Authorization: Bearer …. Stored encrypted.">
          <HeadersEditor rows={headers} onChange={setHeaders} error={shown(problems.headers)} />
        </Fieldset>
      </Disclosure>
    </FormDialog>
  );
}
