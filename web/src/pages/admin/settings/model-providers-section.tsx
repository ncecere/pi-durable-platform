/*
 * Admin › Settings › Team providers and MCP servers: whether teams' owners
 * and admins may add model providers and MCP servers of their own, and the
 * private networks both may be on (public addresses always; loopback,
 * link-local and metadata never). Saved on its own with If-Match on its
 * revision; auditors read.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, api, ifMatch, unwrap } from "@/api/client";
import { type ModelSettings, modelSettingsQuery } from "@/api/models";
import { useRevisionForm } from "@/components/templates/revision-form";
import { SettingsPage, SettingsSection } from "@/components/templates/settings-page";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Field } from "@/components/ui/field/field";
import { SkeletonText } from "@/components/ui/skeleton/skeleton";
import { Switch } from "@/components/ui/switch/switch";
import { TagInput } from "@/components/ui/tag-input/tag-input";
import { toast } from "@/components/ui/toast/toast";
import { useIsPlatformAdmin } from "../hooks";
import { maxNetworks, networksProblem } from "./networks";

const title = "Team providers and MCP servers";
const description =
  "Team owners and admins can add model providers and MCP servers of their own: they're for that team only, and agents reach them through the platform.";
const networksHelp =
  "Teams' own providers and MCP servers must be on public addresses. List private networks (such as 10.20.0.0/16) where teams may also reach them; it applies to both. Loopback, link-local and metadata addresses are never allowed.";

type Form = { teamsMayAddProviders: boolean; teamsMayAddMcpServers: boolean; privateNetworks: string[] };
const formOf = (m: ModelSettings): Form => ({
  teamsMayAddProviders: m.teamsMayAddProviders,
  // On unless turned off (the server always sends it; an older one may not).
  teamsMayAddMcpServers: m.teamsMayAddMcpServers ?? true,
  privateNetworks: m.privateNetworks,
});
const labels = {
  teamsMayAddProviders: "Teams may add their own model providers",
  teamsMayAddMcpServers: "Teams may add their own MCP servers",
  privateNetworks: "Private networks",
};

/** The section, once the settings have loaded. */
export function ModelProviderSettings() {
  const settings = useQuery(modelSettingsQuery());
  if (settings.data) return <ModelProviderForm settings={settings.data} />;
  return (
    <SettingsSection title={title} description={description}>
      {settings.isLoading ? (
        <div role="status" aria-label="Loading…">
          <SkeletonText lines={3} />
        </div>
      ) : (
        <ErrorAlert error={settings.error} title="Couldn't load the team provider and MCP server settings" onRetry={() => void settings.refetch()} />
      )}
    </SettingsSection>
  );
}

function ModelProviderForm({ settings }: { settings: ModelSettings }) {
  const qc = useQueryClient();
  const isAdmin = useIsPlatformAdmin();
  const saved = formOf(settings);
  const [form, setForm, revision] = useRevisionForm(saved, settings.revision, { labels });
  const problem = networksProblem(form.privateNetworks);
  const save = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.PUT("/v1/admin/settings/models", {
          params: { header: ifMatch(settings.revision) },
          body: { teamsMayAddProviders: form.teamsMayAddProviders, teamsMayAddMcpServers: form.teamsMayAddMcpServers, privateNetworks: form.privateNetworks },
        }),
      ),
    onSuccess: (updated) => {
      qc.setQueryData(modelSettingsQuery().queryKey, updated);
      // Whether a team may add a provider or an MCP server now follows the switches.
      void qc.invalidateQueries({ predicate: (q) => q.queryKey[0] === "team" && q.queryKey[2] === "limits" });
      toast.success("Team provider and MCP server settings saved");
    },
  });
  // The server's refusal of a network (public, loopback, link-local) belongs on the field.
  const refused = save.error instanceof ApiError && save.error.code === "invalid_private_networks" ? save.error.message : undefined;
  const set = (patch: Partial<Form>) => {
    if (refused) save.reset();
    setForm((f) => ({ ...f, ...patch }));
  };
  const dirty = JSON.stringify(form) !== JSON.stringify(saved);
  return (
    <SettingsPage
      revision={revision}
      dirty={dirty}
      canEdit={isAdmin}
      readOnlyNote={null}
      saving={save.isPending}
      error={refused ? undefined : save.error}
      saveLabel="Save team provider and MCP server settings"
      onSave={() => save.mutate()}
      onDiscard={() => {
        setForm(saved);
        save.reset();
      }}
      saveDisabled={Boolean(problem)}
      message={problem || refused ? "Not saved: fix the highlighted field" : undefined}
    >
      <SettingsSection title={title} description={description}>
        <Switch
          label={labels.teamsMayAddProviders}
          description="Turned off, teams can't add new providers. Their existing ones keep working and can still be changed or deleted."
          disabled={!isAdmin}
          checked={form.teamsMayAddProviders}
          onCheckedChange={(v) => set({ teamsMayAddProviders: v })}
        />
        <Switch
          label={labels.teamsMayAddMcpServers}
          description="Turned off, teams can't add new MCP servers. Their existing ones keep working and can still be changed or deleted."
          disabled={!isAdmin}
          checked={form.teamsMayAddMcpServers}
          onCheckedChange={(v) => set({ teamsMayAddMcpServers: v })}
        />
        <Field label={labels.privateNetworks} labelHint="Optional" description={`${networksHelp} Press Enter after each network; at most ${maxNetworks}.`} error={problem ?? refused}>
          <TagInput
            value={form.privateNetworks}
            onValueChange={(v) => set({ privateNetworks: v })}
            normalize={(t) => t.trim().toLowerCase()}
            maxTagLength={64}
            noun={{ one: "network", other: "networks" }}
            placeholder={isAdmin ? "10.20.0.0/16" : undefined}
            disabled={!isAdmin}
            spellCheck={false}
            autoComplete="off"
          />
        </Field>
      </SettingsSection>
    </SettingsPage>
  );
}
