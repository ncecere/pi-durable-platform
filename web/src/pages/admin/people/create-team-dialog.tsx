import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useId, useState } from "react";
import { ApiError, api, unwrap, type Schemas } from "@/api/client";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { Dialog, DialogClose } from "@/components/ui/dialog/dialog";
import { Field, Form } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { TextLink } from "@/components/ui/text-link/text-link";
import { toast } from "@/components/ui/toast/toast";
import { emailProblem } from "@/lib/validation";
import { slugify } from "./common";

const empty = { name: "", slug: "", description: "", ownerEmail: "" };
type TeamForm = typeof empty;

/** Creates a team and its owner, then shows who owns it. Stays mounted; `open` controls it. */
export function CreateTeamDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const qc = useQueryClient();
  const formId = useId();
  const [form, setForm] = useState(empty);
  const [slugEdited, setSlugEdited] = useState(false);
  const [done, setDone] = useState<Schemas["TeamCreated"] | null>(null);
  const create = useMutation({
    mutationFn: async () =>
      unwrap(await api.POST("/v1/admin/teams", { body: { name: form.name.trim(), slug: form.slug, description: form.description || undefined, ownerEmail: form.ownerEmail.trim() } })),
    onSuccess: (r) => {
      setDone(r);
      toast.success(`${r.team.team.name} was created`);
      qc.invalidateQueries({ queryKey: ["admin", "teams"] });
      qc.invalidateQueries({ queryKey: ["me"] });
    },
  });
  const slugTaken = create.error instanceof ApiError && create.error.code === "slug_taken";
  const close = (o: boolean) => {
    onOpenChange(o);
    if (!o) {
      setForm(empty);
      setSlugEdited(false);
      setDone(null);
      create.reset();
    }
  };
  return (
    <Dialog
      open={open}
      onOpenChange={close}
      title="Create team"
      description="The owner is added now, or invited if they have never signed in."
      footer={
        done ? (
          <Button onClick={() => close(false)}>Done</Button>
        ) : (
          <>
            <DialogClose>Cancel</DialogClose>
            <Button type="submit" form={formId} loading={create.isPending}>
              Create team
            </Button>
          </>
        )
      }
    >
      {done ? (
        <Alert tone="success" title={`${done.team.team.name} was created.`}>
          {done.owner.status === "added" ? `${done.owner.member?.user.displayName} is its owner.` : `${done.owner.invite?.email} was invited as owner.`}{" "}
          <TextLink render={<Link to="/admin/teams/$team" params={{ team: done.team.team.slug }} onClick={() => close(false)} />}>Open the team</TextLink>
        </Alert>
      ) : (
        <Form
          id={formId}
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <TeamFields
            form={form}
            onName={(name) => setForm({ ...form, name, slug: slugEdited ? form.slug : slugify(name) })}
            slugError={slugTaken ? "A team with this slug already exists. Choose another." : undefined}
            onSlug={(slug) => {
              setSlugEdited(true);
              if (slugTaken) create.reset();
              setForm({ ...form, slug });
            }}
            set={(patch) => setForm({ ...form, ...patch })}
          />
          {/* A taken slug is shown on the Slug field. */}
          {!slugTaken && <ErrorAlert error={create.error} />}
        </Form>
      )}
    </Dialog>
  );
}

type FieldsProps = {
  form: TeamForm;
  /** A server-side problem with the slug (already taken). */
  slugError?: string;
  onName: (v: string) => void;
  onSlug: (v: string) => void;
  set: (patch: Partial<TeamForm>) => void;
};

/** Messages the fields show instead of only a red border; Form validates on submit. */
const slugPattern = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/;
const required = (what: string) => (v: unknown) => (String(v ?? "").trim() ? null : `Enter ${what}.`);

function TeamFields({ form, onName, onSlug, set, slugError }: FieldsProps) {
  return (
    <>
      <Field label="Name" name="name" validate={required("the team's name")}>
        <Input maxLength={100} value={form.name} onChange={(e) => onName(e.target.value)} />
      </Field>
      <Field
        label="Slug"
        description="Used in the team's addresses, such as /teams/platform-tools. It can't be changed later."
        name="slug"
        error={slugError}
        validate={(v) => (!String(v ?? "") ? "Enter a slug." : slugPattern.test(String(v)) ? null : "Use lowercase letters, digits and hyphens, starting and ending with a letter or digit.")}
      >
        <Input value={form.slug} onChange={(e) => onSlug(e.target.value)} />
      </Field>
      <Field label="Description" labelHint="Optional">
        <Textarea maxLength={2000} value={form.description} onChange={(e) => set({ description: e.target.value })} />
      </Field>
      <Field label="Owner's email" name="ownerEmail" validate={(v) => emailProblem(String(v ?? "")) ?? null}>
        <Input type="email" value={form.ownerEmail} onChange={(e) => set({ ownerEmail: e.target.value })} />
      </Field>
    </>
  );
}
