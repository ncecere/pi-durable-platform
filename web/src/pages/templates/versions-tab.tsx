/* A template's versions, newest first; editors add one (new agents get it, existing agents keep theirs). */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useState } from "react";
import { api, errorMessage, unwrap } from "../../api/client";
import { templateVersionsQuery } from "../../api/templates";
import { FormDialog } from "../../components/form-dialog";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { CodeBlock } from "@/components/ui/code-block/code-block";
import { Disclosure } from "@/components/ui/disclosure/disclosure";
import { Field } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { Stack } from "@/components/ui/layout/layout";
import { Time } from "@/components/ui/time/time";
import { toast } from "@/components/ui/toast/toast";
import s from "../shared.module.css";
import {
  type AgentTemplate,
  storeTemplate,
  type TemplateAbilities,
} from "./labels";

export function VersionsTab({
  template,
  abilities,
}: {
  template: AgentTemplate;
  abilities: TemplateAbilities;
}) {
  const versions = useQuery(templateVersionsQuery(template.id));
  const [adding, setAdding] = useState(false);
  return (
    <Card
      title="Versions"
      description="Every change is a new version. Agents keep theirs until someone upgrades them on the agent's page."
      actions={
        abilities.canEdit && !template.archived ? (
          <Button size="sm" onClick={() => setAdding(true)}>
            <Plus aria-hidden /> New version
          </Button>
        ) : undefined
      }
    >
      <Stack gap={3}>
        {(versions.data ?? []).map((v) => (
          <Disclosure
            key={v.version}
            defaultOpen={v.version === template.currentVersion}
            title={
              <span>
                v{v.version}{" "}
                {v.version === template.currentVersion ? (
                  <Badge size="sm" tone="success">
                    Current
                  </Badge>
                ) : null}{" "}
                <span className={s.secondary}>
                  {v.notes || "No notes"} · <Time value={v.createdAt} />
                  {v.createdByName ? ` · ${v.createdByName}` : ""}
                </span>
              </span>
            }
          >
            <CodeBlock code={v.instructions} language="text" hideHeader />
          </Disclosure>
        ))}
        {versions.isError ? (
          <p className={s.dangerText}>{errorMessage(versions.error)}</p>
        ) : null}
      </Stack>
      {adding && (
        <NewVersionDialog
          template={template}
          onClose={() => setAdding(false)}
        />
      )}
    </Card>
  );
}

function NewVersionDialog({
  template,
  onClose,
}: {
  template: AgentTemplate;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const versions = useQuery(templateVersionsQuery(template.id));
  const current = versions.data?.find(
    (v) => v.version === template.currentVersion,
  );
  const [instructions, setInstructions] = useState<string | undefined>(
    undefined,
  );
  const [notes, setNotes] = useState("");
  const [busy, setBusy] = useState(false);
  const value = instructions ?? current?.instructions ?? "";
  const submit = async () => {
    if (!value.trim()) return;
    setBusy(true);
    try {
      const t = unwrap(
        await api.POST("/v1/agent-templates/{templateId}/versions", {
          params: { path: { templateId: template.id } },
          body: { instructions: value, notes },
        }),
      );
      storeTemplate(qc, t);
      void qc.invalidateQueries({
        queryKey: templateVersionsQuery(template.id).queryKey,
      });
      toast.success(
        `v${t.currentVersion} is the current version`,
        "New agents get it; existing agents keep theirs until upgraded.",
      );
      onClose();
    } catch (err) {
      toast.error("Couldn't add the version", errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <FormDialog
      title={`New version of ${template.name}`}
      size="lg"
      onClose={onClose}
      onSubmit={() => void submit()}
      submitLabel={`Save v${template.currentVersion + 1}`}
      busy={busy}
      submitDisabled={!value.trim()}
    >
      <Field label="Instructions">
        <Textarea
          value={value}
          rows={10}
          maxLength={20000}
          onChange={(e) => setInstructions(e.target.value)}
        />
      </Field>
      <Field label="What changed" labelHint="Optional">
        <Input value={notes} maxLength={2000} onValueChange={setNotes} />
      </Field>
    </FormDialog>
  );
}
