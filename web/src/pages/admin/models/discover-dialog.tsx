/* Discover models: what the provider offers, with checkboxes; those already in the catalog are marked and can't be picked again. */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { errorMessage } from "@/api/client";
import { createModel, type DiscoveredModel, discoverQuery, invalidateModels, type ModelProvider, type ModelScope } from "@/api/models";
import { FormDialog } from "@/components/form-dialog";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Checkbox } from "@/components/ui/checkbox/checkbox";
import { Field } from "@/components/ui/field/field";
import { Input } from "@/components/ui/input/input";
import { Loading } from "@/components/ui/spinner/spinner";
import { toast } from "@/components/ui/toast/toast";
import { tokens } from "@/lib/format";
import s from "../../shared.module.css";
import m from "./models.module.css";
import { isTeamOwned } from "./scope";

const matches = (d: DiscoveredModel, q: string) => !q || `${d.name} ${d.id}`.toLowerCase().includes(q.toLowerCase());

type Props = { scope: ModelScope; provider: ModelProvider; onClose: () => void };

export function DiscoverDialog({ scope, provider, onClose }: Props) {
  const qc = useQueryClient();
  const found = useQuery({ ...discoverQuery(scope, provider.id), retry: false });
  const teamOwned = isTeamOwned(scope, provider);
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [filter, setFilter] = useState("");
  const [busy, setBusy] = useState(false);
  const list = found.data ?? [];
  const shown = list.filter((d) => matches(d, filter.trim()));
  const toggle = (id: string, on: boolean) =>
    setPicked((p) => {
      const next = new Set(p);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  const submit = async () => {
    const chosen = list.filter((d) => picked.has(d.id) && !d.added);
    if (chosen.length === 0) return;
    setBusy(true);
    const failed: string[] = [];
    for (const d of chosen) {
      try {
        await createModel(scope, provider.id, { upstreamId: d.id, name: d.name, contextWindow: d.contextWindow ?? undefined });
      } catch (err) {
        failed.push(`${d.name}: ${errorMessage(err)}`);
      }
    }
    await invalidateModels(qc, scope);
    setBusy(false);
    const added = chosen.length - failed.length;
    if (failed.length === 0) {
      toast.success(added === 1 ? "1 model added" : `${added} models added`, `Edit one to change its limits${teamOwned ? " or prices" : ", prices or who may use it"}.`);
      onClose();
    } else {
      toast.error(added ? `${added} added, ${failed.length} not added` : "Couldn't add the models", failed.join("\n"));
      setPicked(new Set());
    }
  };
  const count = [...picked].filter((id) => list.some((d) => d.id === id && !d.added)).length;
  return (
    <FormDialog
      title={`Discover models from ${provider.name}`}
      description={
        teamOwned
          ? `The models the provider offers now. New models are for ${provider.team?.name ?? "the team"} only.`
          : "The models the provider offers now. New models are published to every team; edit one to change that."
      }
      size="lg"
      onClose={onClose}
      onSubmit={() => void submit()}
      submitLabel={count === 1 ? "Add 1 model" : `Add ${count} models`}
      busy={busy}
      submitDisabled={count === 0}
    >
      {found.isLoading ? <Loading label="Asking the provider…" /> : null}
      <ErrorAlert error={found.error} title="Couldn't list the provider's models" onRetry={() => void found.refetch()} />
      {found.data && list.length === 0 ? <p className={s.note}>The provider listed no models. Add them by hand with their model IDs.</p> : null}
      {list.length > 8 ? (
        <Field label="Filter" hideLabel>
          <Input placeholder="Filter by name or ID" value={filter} onValueChange={setFilter} autoComplete="off" />
        </Field>
      ) : null}
      {list.length > 0 ? (
        <div className={m.discovered}>
          {shown.map((d) => (
            <Checkbox
              key={d.id}
              label={d.name}
              description={[d.id, d.contextWindow ? `${tokens(d.contextWindow)} context` : null, d.added ? "Already added" : null].filter(Boolean).join(" · ")}
              disabled={d.added}
              checked={d.added || picked.has(d.id)}
              onCheckedChange={(on) => toggle(d.id, on)}
            />
          ))}
          {shown.length === 0 ? <p className={s.note}>No model matches “{filter}”.</p> : null}
        </div>
      ) : null}
    </FormDialog>
  );
}
