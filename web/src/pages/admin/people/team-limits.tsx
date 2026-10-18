/*
 * Admin team › Limits (personal workspaces): each limit is the platform's
 * default (Admin › Settings) or this workspace's own. Platform admins
 * override one by unticking "Use the default" and typing a value; ticking it
 * again sends null, back to the default. Saved with If-Match on the team's
 * revision (an override changes the team); auditors read.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ifMatch, type Schemas, unwrap } from "@/api/client";
import { personalSettingsQuery, usd, type WorkspaceLimits, workspaceLimitsQuery } from "@/api/personal";
import { useRevisionForm } from "@/components/templates/revision-form";
import { SettingsPage, SettingsSection } from "@/components/templates/settings-page";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Checkbox } from "@/components/ui/checkbox/checkbox";
import { Field } from "@/components/ui/field/field";
import { NumberInput } from "@/components/ui/number-input/number-input";
import { SkeletonText } from "@/components/ui/skeleton/skeleton";
import { toast } from "@/components/ui/toast/toast";
import { adminOnly } from "@/lib/terms";
import { budgetNote, budgetProblem, countProblem, LIMIT_MAX, text } from "../settings/limits";
import p from "./people.module.css";

type Key = "maxAgents" | "maxRunning" | "monthlyBudgetUsd";
type Form = Record<Key, string> & Record<`${Key}Default`, boolean>;

const keys: Key[] = ["maxAgents", "maxRunning", "monthlyBudgetUsd"];

const formOf = (l: WorkspaceLimits): Form => ({
  maxAgents: text(l.maxAgents),
  maxAgentsDefault: !l.overridden.includes("maxAgents"),
  maxRunning: text(l.maxRunning),
  maxRunningDefault: !l.overridden.includes("maxRunning"),
  monthlyBudgetUsd: text(l.monthlyBudgetUsd),
  monthlyBudgetUsdDefault: !l.overridden.includes("monthlyBudgetUsd"),
});

const labels = {
  maxAgents: "Max agents",
  maxAgentsDefault: "Max agents: use the default",
  maxRunning: "Max running at once",
  maxRunningDefault: "Max running at once: use the default",
  monthlyBudgetUsd: "Monthly budget",
  monthlyBudgetUsdDefault: "Monthly budget: use the default",
};

/** What the default is: "5", "$50.00", or "none". */
const defaultText = (key: Key, d: Schemas["PersonalWorkspaceSettings"] | undefined) => {
  if (!d) return "…";
  const v = d[key];
  if (v == null) return "none";
  return key === "monthlyBudgetUsd" ? usd(v) : v.toLocaleString();
};

type Props = { team: Schemas["Team"]; isAdmin: boolean };

export function TeamLimitsTab({ team, isAdmin }: Props) {
  const limits = useQuery(workspaceLimitsQuery(team.slug));
  const defaults = useQuery(personalSettingsQuery());
  if (limits.isLoading) return <SkeletonText lines={4} />;
  if (!limits.data) return <ErrorAlert error={limits.error} title="Couldn't load the limits" onRetry={() => void limits.refetch()} />;
  return <LimitsForm team={team} limits={limits.data} defaults={defaults.data} isAdmin={isAdmin} />;
}

function LimitsForm({ team, limits, defaults, isAdmin }: Props & { limits: WorkspaceLimits; defaults?: Schemas["PersonalWorkspaceSettings"] }) {
  const qc = useQueryClient();
  const saved = formOf(limits);
  const [form, setForm, revision] = useRevisionForm(saved, team.revision, { labels });
  const problems: Partial<Record<Key, string>> = {
    maxAgents: form.maxAgentsDefault ? undefined : countProblem(form.maxAgents, LIMIT_MAX.maxAgents),
    maxRunning: form.maxRunningDefault ? undefined : countProblem(form.maxRunning, LIMIT_MAX.maxRunning),
    monthlyBudgetUsd: form.monthlyBudgetUsdDefault
      ? undefined
      : !form.monthlyBudgetUsd.trim()
        ? "Enter an amount in dollars, or use the default."
        : budgetProblem(form.monthlyBudgetUsd),
  };
  const invalid = Object.values(problems).some(Boolean);
  const save = useMutation({
    mutationFn: async () =>
      unwrap(
        await api.PUT("/v1/admin/teams/{team}/limits", {
          params: { path: { team: team.slug }, header: ifMatch(team.revision) },
          body: {
            maxAgents: form.maxAgentsDefault ? null : Number(form.maxAgents.trim()),
            maxRunning: form.maxRunningDefault ? null : Number(form.maxRunning.trim()),
            monthlyBudgetUsd: form.monthlyBudgetUsdDefault ? null : Number(form.monthlyBudgetUsd.trim()),
          },
        }),
      ),
    onSuccess: (updated) => {
      // The limits first, so the team's new revision finds the form saved.
      qc.setQueryData(workspaceLimitsQuery(team.slug).queryKey, updated);
      void qc.invalidateQueries({ queryKey: ["admin", "team", team.slug] });
      void qc.invalidateQueries({ queryKey: ["team", team.slug] });
      toast.success("Limits saved");
    },
  });
  const dirty = JSON.stringify(form) !== JSON.stringify(saved);
  const set = (patch: Partial<Form>) => setForm((f) => ({ ...f, ...patch }));
  const chooseDefault = (key: Key, on: boolean) => {
    const d = defaults?.[key];
    // Unticked: start from the default, so the field shows what applies now.
    set(on ? { [`${key}Default`]: true } : { [`${key}Default`]: false, [key]: form[key] || text(d) });
  };
  const fields: Record<Key, { label: string; description: string; unit: string; digits: number }> = {
    maxAgents: { label: "Max agents", description: `${limits.agentCount.toLocaleString()} now.`, unit: "agents", digits: 0 },
    maxRunning: { label: "Max running at once", description: `${limits.runningCount.toLocaleString()} running now.`, unit: "agents", digits: 0 },
    monthlyBudgetUsd: { label: "Monthly budget", description: `US dollars per month. ${budgetNote}`, unit: "USD", digits: 2 },
  };
  return (
    <SettingsPage
      revision={revision}
      dirty={dirty}
      canEdit={isAdmin}
      readOnlyNote={adminOnly}
      saving={save.isPending}
      error={save.error}
      saveLabel="Save limits"
      onSave={() => save.mutate()}
      onDiscard={() => setForm(saved)}
      saveDisabled={invalid}
      message={invalid ? "Not saved: fix the highlighted field" : undefined}
    >
      <SettingsSection
        title="Limits"
        description="Each limit is the default for every personal workspace (Admin › Settings) unless this workspace has its own."
      >
        {keys.map((key) => {
          const f = fields[key];
          const isDefault = form[`${key}Default`];
          return (
            <div key={key} className={p.limit}>
              <Field
                label={f.label}
                labelHint={isDefault ? `Default (${defaultText(key, defaults)})` : "This workspace's own"}
                description={f.description}
                error={problems[key]}
              >
                <NumberInput
                  // Following the default: show the default, not a value typed before.
                  value={isDefault ? text(defaults?.[key]) || form[key] : form[key]}
                  maximumFractionDigits={f.digits}
                  unit={f.unit}
                  disabled={!isAdmin || isDefault}
                  onValueChange={(v) => set({ [key]: v })}
                />
              </Field>
              <Checkbox
                label="Use the default"
                checked={isDefault}
                disabled={!isAdmin}
                onCheckedChange={(on) => chooseDefault(key, on)}
              />
            </div>
          );
        })}
      </SettingsSection>
    </SettingsPage>
  );
}
