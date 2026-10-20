/* A template's versions, newest first; editors add one (new agents get it, existing agents keep theirs). */
import { useQuery } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useState } from "react";
import { errorMessage } from "../../api/client";
import {
  type AgentTemplateVersion,
  templateVersionsQuery,
} from "../../api/templates";
import { mcpSummary } from "../../api/mcp";
import { modelLabel } from "../../components/model-select";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { CodeBlock } from "@/components/ui/code-block/code-block";
import { Disclosure } from "@/components/ui/disclosure/disclosure";
import { Stack } from "@/components/ui/layout/layout";
import { Time } from "@/components/ui/time/time";
import s from "../shared.module.css";
import { type AgentTemplate, type TemplateAbilities } from "./labels";
import { NewVersionDialog } from "./new-version-dialog";

/** The version's model: "Fake One · Provider", or "Platform default". */
const modelOf = (v: AgentTemplateVersion) =>
  v.modelId
    ? modelLabel({
        name: v.modelName ?? "Unknown model",
        providerName: v.modelProviderName ?? "",
      })
    : "Platform default";

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
                <Badge size="sm" title="The model it runs on">
                  {modelOf(v)}
                </Badge>{" "}
                {(v.mcp ?? []).map((a) => (
                  <span key={a.serverId}>
                    <Badge size="sm" title="An MCP server its agents can call">
                      {mcpSummary(a)}
                    </Badge>{" "}
                  </span>
                ))}
                {v.clientMcp ? (
                  <>
                    <Badge
                      size="sm"
                      title="Apps can give its agents their own MCP servers for a thread"
                    >
                      Apps may add MCP servers
                    </Badge>{" "}
                  </>
                ) : null}
                {v.approvals?.tools.length ? (
                  <>
                    <Badge size="sm" tone="warning" title={`A person approves these tools before they run: ${v.approvals.tools.join(", ")}`}>
                      Asks before {v.approvals.tools.length === 1 ? v.approvals.tools[0] : `${v.approvals.tools.length} tools`}
                    </Badge>{" "}
                  </>
                ) : null}
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
