/*
 * Add a trigger to an agent (its team's owners and admins): a schedule (cron in a time zone, and what to do about a
 * time missed while the platform was down) or a webhook (an address and a secret, an optional filter), the message
 * each fire sends, an optional completion callback and a daily budget. The secrets are shown once, right after.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ApiError, errorMessage } from "../../../api/client";
import { createTrigger, type TriggerCreate, type TriggerSaved, triggersKey } from "../../../api/triggers";
import { FormDialog } from "../../../components/form-dialog";
import { Alert } from "@/components/ui/alert/alert";
import { Button } from "@/components/ui/button/button";
import { CopyField } from "@/components/ui/copy-field/copy-field";
import { Dialog } from "@/components/ui/dialog/dialog";
import { Field } from "@/components/ui/field/field";
import { Input, Textarea } from "@/components/ui/input/input";
import { Stack } from "@/components/ui/layout/layout";
import { Select } from "@/components/ui/select/select";
import type { Agent } from "./labels";

type Kind = TriggerCreate["kind"];

/** Which field a refusal is about. */
const fieldOf: Record<string, string> = {
  invalid_trigger_name: "name",
  trigger_name_taken: "name",
  invalid_cron: "cron",
  invalid_timezone: "timezone",
  invalid_message_template: "template",
  invalid_filter: "filter",
  invalid_callback_url: "callback",
  address_not_allowed: "callback",
  address_unresolved: "callback",
  invalid_budget: "budget",
};

export function AddTriggerDialog({ agent, onClose }: { agent: Agent; onClose: () => void }) {
  const qc = useQueryClient();
  const [kind, setKind] = useState<Kind>("schedule");
  const [name, setName] = useState("");
  const [cron, setCron] = useState("0 8 * * 1-5");
  const [timezone, setTimezone] = useState(() => Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC");
  const [missed, setMissed] = useState<"skip" | "run_once">("skip");
  const [template, setTemplate] = useState("");
  const [filterPath, setFilterPath] = useState("");
  const [filterEquals, setFilterEquals] = useState("");
  const [callback, setCallback] = useState("");
  const [budget, setBudget] = useState("");
  const [problem, setProblem] = useState<{ field: string; message: string }>();
  const create = useMutation({
    mutationFn: () => {
      const body: TriggerCreate = { kind, name: name.trim(), messageTemplate: template };
      if (kind === "schedule") Object.assign(body, { cron: cron.trim(), timezone: timezone.trim(), missedPolicy: missed });
      if (kind === "webhook" && filterPath.trim()) body.filter = { path: filterPath.trim(), equals: filterEquals };
      if (callback.trim()) body.callbackUrl = callback.trim();
      if (budget.trim()) body.dailyBudgetUsd = Number(budget);
      return createTrigger(agent.id, body);
    },
    onSuccess: () => void qc.invalidateQueries({ queryKey: triggersKey(agent.id) }),
    onError: (err) => {
      const field = err instanceof ApiError ? fieldOf[err.code] : undefined;
      setProblem({ field: field ?? "", message: errorMessage(err) });
    },
  });
  const error = (field: string) => (problem?.field === field ? problem.message : undefined);

  if (create.data) return <TriggerSecrets saved={create.data} onClose={onClose} />;
  return (
    <FormDialog
      title={`Add a trigger to ${agent.name}`}
      description="Each fire sends a message to a new conversation, as the trigger (never as a person), with the team's models and tools."
      size="lg"
      onClose={onClose}
      onSubmit={() => {
        setProblem(undefined);
        create.mutate();
      }}
      submitLabel="Add trigger"
      busy={create.isPending}
      submitDisabled={!name.trim() || !template.trim()}
    >
      {problem && !problem.field ? <Alert tone="danger">{problem.message}</Alert> : null}
      <Select<Kind>
        label="Kind"
        items={[
          { value: "schedule", label: "Schedule", hint: "At times you set" },
          { value: "webhook", label: "Webhook", hint: "When a signed request arrives" },
        ]}
        value={kind}
        onValueChange={(v) => v && setKind(v)}
      />
      <Field label="Name" error={error("name")}>
        <Input value={name} onValueChange={setName} maxLength={100} autoComplete="off" />
      </Field>
      {kind === "schedule" ? (
        <>
          <Field label="Schedule" description="Cron: minute hour day month weekday (0 8 * * 1-5 is 8:00 on weekdays), or @daily, @hourly…" error={error("cron")}>
            <Input value={cron} onValueChange={setCron} maxLength={200} autoComplete="off" />
          </Field>
          <Field label="Time zone" description="An IANA time zone, such as Europe/Paris or UTC." error={error("timezone")}>
            <Input value={timezone} onValueChange={setTimezone} maxLength={64} autoComplete="off" />
          </Field>
          <Select<"skip" | "run_once">
            label="A time missed while the platform was down"
            items={[
              { value: "skip", label: "Skip it" },
              { value: "run_once", label: "Run once when it's back" },
            ]}
            value={missed}
            onValueChange={(v) => v && setMissed(v)}
          />
        </>
      ) : (
        <>
          <Field
            label="Only when the field"
            labelHint="Optional"
            description="A field of the request's JSON, such as action or pull_request.state: runs start only when it has the value below."
            error={error("filter")}
          >
            <Input value={filterPath} onValueChange={setFilterPath} placeholder="action" maxLength={200} autoComplete="off" />
          </Field>
          <Field label="has the value" labelHint="Optional">
            <Input value={filterEquals} onValueChange={setFilterEquals} placeholder="opened" maxLength={1000} autoComplete="off" />
          </Field>
        </>
      )}
      <Field
        label="Message"
        description={
          kind === "webhook"
            ? "What the agent is asked. {{payload.title}} inserts a field of the request, {{payload}} all of it, {{fire.time}} the time."
            : "What the agent is asked. {{fire.time}} inserts the scheduled time, {{trigger.name}} the trigger's name."
        }
        error={error("template")}
      >
        <Textarea value={template} onChange={(e) => setTemplate(e.target.value)} maxLength={8000} rows={4} />
      </Field>
      <Field label="Completion callback" labelHint="Optional" description="An https address called, signed, when each run ends." error={error("callback")}>
        <Input value={callback} onValueChange={setCallback} placeholder="https://" maxLength={2000} autoComplete="off" />
      </Field>
      <Field label="Daily budget (USD)" labelHint="Optional" description="Runs stop, and fires are skipped, once its runs cost this much in a day (UTC)." error={error("budget")}>
        <Input value={budget} onValueChange={setBudget} inputMode="decimal" autoComplete="off" />
      </Field>
    </FormDialog>
  );
}

/** A new trigger's address and secrets, shown once. */
export function TriggerSecrets({ saved, onClose }: { saved: TriggerSaved; onClose: () => void }) {
  const t = saved.trigger;
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={`${t.name} is set`} size="lg" footer={<Button onClick={onClose}>Done</Button>}>
      <Stack gap={5}>
        {saved.webhookSecret || saved.callbackSecret ? (
          <Alert tone="warning" title="Copy the secrets now.">
            They won&apos;t be shown again. If one is lost, replace it from the trigger&apos;s menu.
          </Alert>
        ) : null}
        {t.webhookUrl ? <CopyField label="Webhook address" name="webhook address" value={t.webhookUrl} description="The sender POSTs here." /> : null}
        {saved.webhookSecret ? (
          <CopyField
            label="Webhook secret"
            name="webhook secret"
            value={saved.webhookSecret}
            description='The sender signs each request: X-PDP-Signature: t=<unix seconds>,v1=<hex HMAC-SHA256 of "<t>." and the body>.'
          />
        ) : null}
        {saved.callbackSecret ? (
          <CopyField
            label="Callback secret"
            name="callback secret"
            value={saved.callbackSecret}
            description="Each callback is signed the same way with this secret: check it before trusting a callback."
          />
        ) : null}
        {!t.webhookUrl && !saved.callbackSecret ? <p>Its first run is at its next scheduled time.</p> : null}
      </Stack>
    </Dialog>
  );
}
