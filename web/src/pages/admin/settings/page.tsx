/*
 * Admin › Settings (/admin/settings): platform-wide settings. Personal
 * workspaces: whether everyone who signs in gets one, and the limits every
 * one starts with (max agents, max running at once, a monthly budget that is
 * recorded but not enforced yet) and whether people may add model providers
 * and MCP servers in theirs. Team providers and MCP servers: whether teams
 * may add their own, and the private networks both may be on
 * (model-providers-section.tsx). Trusted
 * apps: the OAuth Connect apps that skip the approval screen
 * (trusted-apps-section.tsx). Each section saves on
 * its own with If-Match on its revision; platform auditors see them
 * read-only.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ifMatch, unwrap } from "@/api/client";
import { type PersonalSettings, personalSettingsQuery } from "@/api/personal";
import { useRevisionForm } from "@/components/templates/revision-form";
import { SettingsPage, SettingsSection } from "@/components/templates/settings-page";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Field } from "@/components/ui/field/field";
import { Stack } from "@/components/ui/layout/layout";
import { NumberInput } from "@/components/ui/number-input/number-input";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { SkeletonText } from "@/components/ui/skeleton/skeleton";
import { Switch } from "@/components/ui/switch/switch";
import { toast } from "@/components/ui/toast/toast";
import { adminOnly } from "@/lib/terms";
import s from "../../shared.module.css";
import { useIsPlatformAdmin } from "../hooks";
import { budgetNote, budgetOf, budgetProblem, countProblem, LIMIT_MAX, text } from "./limits";
import { ModelProviderSettings } from "./model-providers-section";
import { AppSecretsSettings } from "./app-secrets-section";
import { TrustedAppsSettings } from "./trusted-apps-section";

type Form = { enabled: boolean; ownProviders: boolean; ownMcpServers: boolean; maxAgents: string; maxRunning: string; monthlyBudgetUsd: string };

const formOf = (p: PersonalSettings): Form => ({
  enabled: p.enabled,
  ownProviders: p.ownProviders ?? false,
  ownMcpServers: p.ownMcpServers ?? false,
  maxAgents: text(p.maxAgents),
  maxRunning: text(p.maxRunning),
  monthlyBudgetUsd: text(p.monthlyBudgetUsd),
});
const labels = { enabled: "Personal workspaces", ownProviders: "Own model providers", ownMcpServers: "Own MCP servers", maxAgents: "Max agents", maxRunning: "Max running at once", monthlyBudgetUsd: "Monthly budget" };

/** /admin/settings */
export function AdminSettingsPage() {
  const settings = useQuery(personalSettingsQuery());
  const isAdmin = useIsPlatformAdmin();
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader title="Settings" description="Settings for the whole platform." />
      {/* Said once for every section. */}
      {!isAdmin && <Alert tone="info">{adminOnly}</Alert>}
      {settings.isLoading ? (
        <div role="status" aria-label="Loading…">
          <SkeletonText lines={4} />
        </div>
      ) : settings.data ? (
        <PersonalWorkspaceSettings settings={settings.data} />
      ) : (
        <ErrorAlert error={settings.error} title="Couldn't load the settings" onRetry={() => void settings.refetch()} />
      )}
      <ModelProviderSettings />
      <TrustedAppsSettings />
      <AppSecretsSettings />
    </Stack>
  );
}

function PersonalWorkspaceSettings({ settings }: { settings: PersonalSettings }) {
  const qc = useQueryClient();
  const isAdmin = useIsPlatformAdmin();
  const saved = formOf(settings);
  const [form, setForm, revision] = useRevisionForm(saved, settings.revision, { labels });
  const problems = {
    maxAgents: countProblem(form.maxAgents, LIMIT_MAX.maxAgents),
    maxRunning: countProblem(form.maxRunning, LIMIT_MAX.maxRunning),
    monthlyBudgetUsd: budgetProblem(form.monthlyBudgetUsd),
  };
  const invalid = Object.values(problems).some(Boolean);
  const save = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.PUT("/v1/admin/settings/personal-workspaces", {
          params: { header: ifMatch(settings.revision) },
          body: {
            enabled: form.enabled,
            ownProviders: form.ownProviders,
            ownMcpServers: form.ownMcpServers,
            maxAgents: Number(form.maxAgents.trim()),
            maxRunning: Number(form.maxRunning.trim()),
            monthlyBudgetUsd: budgetOf(form.monthlyBudgetUsd),
          },
        }),
      ),
    onSuccess: (updated) => {
      qc.setQueryData(personalSettingsQuery().queryKey, updated);
      // Every personal workspace's limits (and whether it may add a provider) may have changed.
      void qc.invalidateQueries({ predicate: (q) => q.queryKey[0] === "team" && q.queryKey[2] === "limits" });
      toast.success("Settings saved");
    },
  });
  const dirty = JSON.stringify(form) !== JSON.stringify(saved);
  const set = (patch: Partial<Form>) => setForm((f) => ({ ...f, ...patch }));
  return (
    <SettingsPage
      revision={revision}
      dirty={dirty}
      canEdit={isAdmin}
      readOnlyNote={null}
      saving={save.isPending}
      error={save.error}
      onSave={() => save.mutate()}
      onDiscard={() => setForm(saved)}
      saveDisabled={invalid}
      message={invalid ? "Not saved: fix the highlighted field" : undefined}
    >
      <SettingsSection
        title="Personal workspaces"
        description="A workspace of their own for each person: only they use it, and they deploy agents from the catalog. Each one starts with these limits; a workspace's page in Admin › Teams can change them for that workspace."
      >
        <Switch
          label="Give everyone who signs in a personal workspace"
          description="Turned off, no new personal workspaces are created. Existing ones stay: archive one in Admin › Teams to make it read-only."
          disabled={!isAdmin}
          checked={form.enabled}
          onCheckedChange={(v) => set({ enabled: v })}
        />
        <Switch
          label="People may add their own model providers in their personal workspace"
          description="Their models are for that workspace only. It also needs “Teams may add their own model providers” turned on, under Team providers and MCP servers."
          disabled={!isAdmin}
          checked={form.ownProviders}
          onCheckedChange={(v) => set({ ownProviders: v })}
        />
        <Switch
          label="People may add their own MCP servers in their personal workspace"
          description="Their servers are for that workspace only. It also needs “Teams may add their own MCP servers” turned on, under Team providers and MCP servers."
          disabled={!isAdmin}
          checked={form.ownMcpServers}
          onCheckedChange={(v) => set({ ownMcpServers: v })}
        />
        <Field label="Max agents" description="Agents one personal workspace may have." error={problems.maxAgents}>
          <NumberInput value={form.maxAgents} maximumFractionDigits={0} disabled={!isAdmin} unit="agents" onValueChange={(v) => set({ maxAgents: v })} />
        </Field>
        <Field label="Max running at once" description="Agents awake at the same time. Waking another is refused until one hibernates." error={problems.maxRunning}>
          <NumberInput value={form.maxRunning} maximumFractionDigits={0} disabled={!isAdmin} unit="agents" onValueChange={(v) => set({ maxRunning: v })} />
        </Field>
        <Field label="Monthly budget" labelHint="Optional" description={`US dollars per workspace per month. ${budgetNote}`} error={problems.monthlyBudgetUsd}>
          <NumberInput value={form.monthlyBudgetUsd} maximumFractionDigits={2} disabled={!isAdmin} unit="USD" onValueChange={(v) => set({ monthlyBudgetUsd: v })} />
        </Field>
      </SettingsSection>
    </SettingsPage>
  );
}
