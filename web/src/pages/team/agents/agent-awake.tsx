/*
 * Why an awake agent isn't hibernating (docs/hibernation.md): the agent's `awake`, from its runtime's own report. A
 * reply in progress, someone watching a conversation, background work (under the platform's time limit), or recent
 * use. Work waiting for someone's answer is shown too, but it doesn't keep the agent awake.
 */
import { Time } from "@/components/ui/time/time";
import s from "../../shared.module.css";
import type { Agent } from "./labels";

type Awake = NonNullable<Agent["awake"]>;

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

/** The reasons, in words. */
export function awakeReasons(awake: Awake): string[] {
  const out: string[] = [];
  for (const reason of awake.reasons) {
    if (reason === "reply") out.push("a reply is in progress");
    if (reason === "stream") out.push("someone is watching a conversation");
    if (reason === "background") out.push(`background work (${plural(awake.backgroundTasks, "task", "tasks")})`);
    if (reason === "recent") out.push("it was used in the last few minutes");
  }
  return out;
}

/** The status, and under it what keeps the agent awake. */
export function AwakeValue({ agent, label }: { agent: Agent; label: string }) {
  const awake = agent.running && agent.enabled ? agent.awake : undefined;
  if (!awake) return <span>{label}</span>;
  const reasons = awakeReasons(awake);
  return (
    <span>
      <span>{label}</span>
      <span className={s.secondary}>
        {reasons.length > 0
          ? `Kept awake by ${reasons.join(", ")}.`
          : "Nothing keeps it awake: it hibernates shortly."}
        {awake.reasons.includes("background") && awake.backgroundSince ? (
          <>
            {" "}
            The oldest started <Time value={awake.backgroundSince} format="relative" />.
          </>
        ) : null}
      </span>
      {awake.overCeiling ? (
        <span className={s.secondary}>
          Background work has run past the platform&apos;s time limit: it&apos;s stopped before the agent hibernates.
        </span>
      ) : null}
      {awake.waitingForPerson > 0 ? (
        <span className={s.secondary}>
          {plural(awake.waitingForPerson, "task waits", "tasks wait")} for someone&apos;s answer. That doesn&apos;t
          keep it awake: the work goes on when the answer comes.
        </span>
      ) : null}
    </span>
  );
}
