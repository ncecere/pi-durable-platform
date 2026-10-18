/*
 * Add a model by hand (?form=new) or edit one (?form=<model id>) on a form
 * page over the provider: its limits, capabilities, prices, switch and who
 * may use it. Edits are saved with If-Match on the model's revision.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { ApiError } from "@/api/client";
import { createModel, deleteModel, invalidateModels, type Model, type ModelProvider, type ModelScope, providerModelsQuery, updateModel } from "@/api/models";
import { ConfirmMutationDialog } from "@/components/confirm-dialog";
import { FormPage } from "@/components/templates/form-page";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { toast } from "@/components/ui/toast/toast";
import { bodyOf, type ModelForm, formOf, newModelForm, problemsOf, updateOf } from "./model-form-state";
import { ModelFields } from "./model-fields";
import { isTeamOwned } from "./scope";

type HostProps = { scope: ModelScope; provider: ModelProvider; id: string; onClose: () => void };

/** The form for ?form=: a new model, or the provider's model with that id once the list has loaded. */
export function ModelFormHost({ scope, provider, id, onClose }: HostProps) {
  const models = useQuery({ ...providerModelsQuery(scope, provider.id), enabled: id !== "new" });
  if (id === "new") return <ModelFormPage scope={scope} provider={provider} onClose={onClose} />;
  const model = models.data?.find((x) => x.id === id);
  if (!model)
    return (
      <FormPage label="Edit model" title="Edit model" onClose={onClose} onSubmit={() => {}} submitLabel="Save model" loading={models.isLoading} submitDisabled>
        {models.isLoading ? null : <Alert tone="warning">This model isn&apos;t in {provider.name} any more.</Alert>}
      </FormPage>
    );
  return <ModelFormPage scope={scope} provider={provider} model={model} onClose={onClose} />;
}

function ModelFormPage({ scope, provider, model, onClose }: { scope: ModelScope; provider: ModelProvider; model?: Model; onClose: () => void }) {
  const qc = useQueryClient();
  // A team's own provider's models are its team's only: nothing to publish.
  const publishing = !isTeamOwned(scope, provider);
  const formRef = useRef<HTMLFormElement>(null);
  const [form, setForm] = useState<ModelForm>(() => (model ? formOf(model) : newModelForm));
  const [submitted, setSubmitted] = useState(false);
  const [done, setDone] = useState(false);
  const [deleting, setDeleting] = useState<Model | null>(null);
  const problems = problemsOf(form, !model, publishing);
  const save = useMutation({
    mutationFn: async () => (model ? updateModel(scope, model, updateOf(form, model, publishing)) : createModel(scope, provider.id, bodyOf(form, publishing))),
    onSuccess: (saved) => {
      toast.success(model ? `${saved.name} was saved` : `${saved.name} was added`, saved.enabled ? undefined : "It's turned off: teams can't choose it yet.");
      void invalidateModels(qc, scope);
      setDone(true);
    },
  });
  const del = useMutation({
    mutationFn: async (x: Model) => deleteModel(scope, x.id),
    onSuccess: (_, x) => {
      setDeleting(null);
      toast.success(`${x.name} was deleted`);
      void invalidateModels(qc, scope);
      setDone(true);
    },
  });
  // Saved: the form no longer counts as unsaved in this render, so closing doesn't ask.
  useEffect(() => {
    if (done) onClose();
  }, [done, onClose]);
  const taken = save.error instanceof ApiError && save.error.code === "model_taken";
  const submit = () => {
    setSubmitted(true);
    if (Object.values(problems).some(Boolean)) {
      requestAnimationFrame(() => formRef.current?.querySelector<HTMLElement>('[aria-invalid="true"]')?.focus());
      return;
    }
    save.mutate();
  };
  const title = model ? `Edit ${model.name}` : `Add a model to ${provider.name}`;
  return (
    <FormPage
      label={model ? model.name : "Add model"}
      title={title}
      description={model ? undefined : "Use the provider's ID for the model, as its API expects it."}
      onClose={onClose}
      onSubmit={submit}
      submitLabel={model ? "Save model" : "Add model"}
      busy={save.isPending}
      dirty={done ? false : undefined}
      formRef={formRef}
      startActions={
        model ? (
          <Button variant="danger" onClick={() => setDeleting(model)}>
            Delete model
          </Button>
        ) : undefined
      }
    >
      <ModelFields
        form={form}
        setForm={(f) => {
          if (taken) save.reset();
          setForm(f);
        }}
        isNew={!model}
        currentTeams={model?.teams ?? []}
        teamOnly={publishing ? undefined : (provider.team?.name ?? "the team")}
        errors={{
          ...(submitted ? problems : {}),
          upstreamId: (submitted ? problems.upstreamId : undefined) ?? (taken ? `${provider.name} already has a model with this ID.` : undefined),
        }}
      />
      <ErrorAlert error={taken ? undefined : save.error} title={model ? "Couldn't save the model" : "Couldn't add the model"} />
      <ConfirmMutationDialog
        target={deleting}
        onClose={() => setDeleting(null)}
        mutation={del}
        onConfirm={(x) => del.mutate(x)}
        title={`Delete ${model?.name ?? "model"}?`}
        description="Teams can't choose it any more. A model templates or agents still use can't be deleted: move them to another model, or turn it off instead."
        confirmLabel="Delete model"
      />
    </FormPage>
  );
}
