/*
 * Template lists: the Catalog (everyone), a team's Templates and the admin
 * Templates page (the review queue, then every template).
 */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { BookOpen, ClipboardCheck, Plus } from "lucide-react";
import { useId, useState } from "react";
import {
  adminTemplatesQuery,
  catalogQuery,
  teamTemplatesQuery,
} from "../../api/templates";
import {
  ListPage,
  RelativeTime,
  timeColumn,
} from "../../components/templates/list-page";
import { useCurrentUser } from "../../session";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import {
  CellText,
  type DataTableColumn,
} from "@/components/ui/data-table/data-table";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { TextLink } from "@/components/ui/text-link/text-link";
import s from "../shared.module.css";
import { useTeam } from "../team/common";
import { CreateTemplateDialog } from "./create-dialog";
import {
  type AgentTemplate,
  ownerLabel,
  statusLabel,
  statusTone,
  whatIsATemplate,
} from "./labels";

const link = (t: AgentTemplate) => (
  <Link to="/templates/$templateId" params={{ templateId: t.id }} />
);

function columns(opts: {
  owner?: boolean;
  status?: boolean;
}): DataTableColumn<AgentTemplate>[] {
  return [
    {
      id: "name",
      header: "Template",
      accessor: (t) => t.name,
      rowHeader: true,
      hideable: false,
      sortable: true,
      cell: (t) => (
        <CellText
          className={s.compactCell}
          wrap="anywhere"
          primary={<TextLink render={link(t)}>{t.name}</TextLink>}
          secondary={
            t.description ? (
              <span className={s.truncate}>{t.description}</span>
            ) : (
              <span className={s.mono}>{t.slug}</span>
            )
          }
        />
      ),
    },
    ...(opts.owner
      ? [
          {
            id: "owner",
            header: "Owner",
            accessor: ownerLabel,
            sortable: true,
          } as DataTableColumn<AgentTemplate>,
        ]
      : []),
    ...(opts.status
      ? [
          {
            id: "status",
            header: "Status",
            accessor: (t: AgentTemplate) =>
              t.archived ? "Archived" : statusLabel[t.status],
            sortable: true,
            cell: (t: AgentTemplate) =>
              t.archived ? (
                <Badge tone="neutral">Archived</Badge>
              ) : (
                <Badge tone={statusTone[t.status]}>
                  {statusLabel[t.status]}
                </Badge>
              ),
          } as DataTableColumn<AgentTemplate>,
        ]
      : []),
    {
      id: "version",
      header: "Version",
      accessor: (t) => t.currentVersion,
      sortable: true,
      cell: (t) => <span className={s.nowrap}>v{t.currentVersion}</span>,
    },
    {
      ...timeColumn<AgentTemplate>("updatedAt", "Updated", (t) => t.updatedAt),
      cell: (t) => (
        <span className={s.nowrap}>
          <RelativeTime value={t.updatedAt} />
        </span>
      ),
    },
  ];
}

/** /catalog: platform templates and templates teams published. */
export function CatalogPage() {
  const me = useCurrentUser();
  const templates = useQuery(catalogQuery());
  const [creating, setCreating] = useState(false);
  const create = me.capabilities.platformAdmin && (
    <Button variant="secondary" onClick={() => setCreating(true)}>
      <Plus aria-hidden /> New platform template
    </Button>
  );
  return (
    <>
      <ListPage<AgentTemplate>
        id="catalog"
        title="Catalog"
        description="Templates every team can deploy: the platform's, and those teams published after review. A team's own templates are on its Templates page."
        primaryAction={create}
        caption="Catalog"
        columns={columns({ owner: true })}
        data={templates.data ?? []}
        getRowId={(t) => t.id}
        rowLabel={(t) => t.name}
        search={{
          label: "Search templates",
          placeholder: "Name, owner or description",
        }}
        loading={templates.isLoading}
        error={templates.error}
        onRetry={() => void templates.refetch()}
        rowActions={(t) => [{ label: "Open", render: link(t) }]}
        empty={{
          icon: <BookOpen />,
          title: "The catalog is empty.",
          description: whatIsATemplate,
        }}
      />
      {creating && <CreateTemplateDialog onClose={() => setCreating(false)} />}
    </>
  );
}

/** /teams/$team/templates: the team's own templates. */
export function TeamTemplatesPage() {
  const { slug, team, canEdit, personal } = useTeam();
  const templates = useQuery(teamTemplatesQuery(slug));
  const [creating, setCreating] = useState(false);
  // A personal workspace deploys catalog templates only (the server refuses its own).
  const create = canEdit && !personal && (
    <Button onClick={() => setCreating(true)}>
      <Plus aria-hidden /> New template
    </Button>
  );
  return (
    <>
      <ListPage<AgentTemplate>
        id="team-templates"
        title="Templates"
        description={`${team.name}'s own templates: private to the team until its owners or admins publish one to the catalog, after a platform admin's review.`}
        primaryAction={create}
        caption="Templates"
        columns={columns({ status: true })}
        data={templates.data ?? []}
        getRowId={(t) => t.id}
        rowLabel={(t) => t.name}
        search={{
          label: "Search templates",
          placeholder: "Name or description",
        }}
        loading={templates.isLoading}
        error={templates.error}
        onRetry={() => void templates.refetch()}
        rowActions={(t) => [{ label: "Open", render: link(t) }]}
        empty={{
          icon: <BookOpen />,
          title: "No templates yet.",
          description: whatIsATemplate,
          action: create || undefined,
        }}
      />
      {creating && (
        <CreateTemplateDialog team={slug} onClose={() => setCreating(false)} />
      )}
    </>
  );
}

/** /admin/templates: the review queue, then every template, each under its own heading. */
export function AdminTemplatesPage() {
  const me = useCurrentUser();
  const pending = useQuery(adminTemplatesQuery("pending"));
  const all = useQuery(adminTemplatesQuery());
  const [creating, setCreating] = useState(false);
  const pendingId = useId();
  const allId = useId();
  return (
    <Stack gap={8} className={s.page}>
      <PageHeader
        title="Templates"
        description="Teams ask to publish their templates to the catalog; platform admins review them here."
        actions={
          me.capabilities.platformAdmin && (
            <Button variant="secondary" onClick={() => setCreating(true)}>
              <Plus aria-hidden /> New platform template
            </Button>
          )
        }
      />
      <section aria-labelledby={pendingId} className={s.section}>
        <div className={s.sectionHeading}>
          <h2 id={pendingId} className={s.sectionTitle}>
            Waiting for review
          </h2>
          <p className={s.settingDescription}>Team templates whose owners asked to publish them to the catalog.</p>
        </div>
        <ListPage<AgentTemplate>
          id="admin-templates-pending"
          caption="Templates waiting for review"
          columns={columns({ owner: true })}
          data={pending.data ?? []}
          getRowId={(t) => t.id}
          rowLabel={(t) => t.name}
          loading={pending.isLoading}
          error={pending.error}
          onRetry={() => void pending.refetch()}
          rowActions={(t) => [{ label: "Review", render: link(t) }]}
          empty={{
            icon: <ClipboardCheck />,
            title: "Nothing waiting for review.",
            description: "Requests to publish team templates show here.",
          }}
        />
      </section>
      <section aria-labelledby={allId} className={s.section}>
        <div className={s.sectionHeading}>
          <h2 id={allId} className={s.sectionTitle}>
            Every template
          </h2>
          <p className={s.settingDescription}>The platform&apos;s templates and every team&apos;s, published or not.</p>
        </div>
        <ListPage<AgentTemplate>
          id="admin-templates-all"
          caption="Every template"
          columns={columns({ owner: true, status: true })}
          data={all.data ?? []}
          getRowId={(t) => t.id}
          rowLabel={(t) => t.name}
          search={{
            label: "Search templates",
            placeholder: "Name, owner or description",
          }}
          loading={all.isLoading}
          error={all.error}
          onRetry={() => void all.refetch()}
          rowActions={(t) => [{ label: "Open", render: link(t) }]}
          empty={{
            icon: <BookOpen />,
            title: "No templates.",
            description: whatIsATemplate,
          }}
        />
      </section>
      {creating && <CreateTemplateDialog onClose={() => setCreating(false)} />}
    </Stack>
  );
}
