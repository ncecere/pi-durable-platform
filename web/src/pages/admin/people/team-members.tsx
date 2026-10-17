/*
 * Admin team › Members: the read-only member list (team autonomy: owners
 * manage their members) and "Assign owner", which picks someone who has
 * signed in or invites an email address. Open invites follow: an owner
 * invited by email shows there, and platform admins can revoke it.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { UserPlus, UserRound } from "lucide-react";
import { useState } from "react";
import { api, unwrap, type Schemas } from "@/api/client";
import { membersQuery } from "@/api/queries";
import { InviteList } from "@/components/members";
import { Stack } from "@/components/ui/layout/layout";
import { PersonCell } from "@/components/person-cell";
import { roleLabels } from "@/components/roles";
import { ListPage } from "@/components/templates/list-page";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { Combobox, type ComboboxOption } from "@/components/ui/combobox/combobox";
import { CellText, type DataTableColumn } from "@/components/ui/data-table/data-table";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Dialog, DialogClose } from "@/components/ui/dialog/dialog";
import { Field, Form } from "@/components/ui/field/field";
import { toast } from "@/components/ui/toast/toast";
import { looksLikeEmail } from "@/lib/validation";
import { useDebounced } from "../hooks";

type Member = Schemas["Member"];

const columns: DataTableColumn<Member>[] = [
  {
    id: "person",
    header: "Person",
    accessor: (m) => `${m.user.displayName} ${m.user.email}`,
    sortable: true,
    rowHeader: true,
    cell: (m) => (
      <PersonCell name={m.user.displayName || m.user.email}>
        <CellText primary={m.user.displayName || m.user.email} secondary={m.user.displayName ? m.user.email : undefined} />
      </PersonCell>
    ),
  },
  {
    id: "role",
    header: "Role",
    accessor: (m) => roleLabels[m.role],
    sortable: true,
    cell: (m) => <Badge tone={m.role === "owner" ? "info" : "neutral"}>{roleLabels[m.role]}</Badge>,
  },
];

export function TeamMembersTab({ team, isAdmin }: { team: string; isAdmin: boolean }) {
  const [assigning, setAssigning] = useState(false);
  const members = useQuery(membersQuery(team));
  return (
    <Stack gap={6}>
      <Card
        title="Members"
        description="Owners and admins manage their team's members. Platform admins assign owners."
        actions={
          isAdmin && (
            <Button size="sm" variant="secondary" onClick={() => setAssigning(true)}>
              <UserPlus aria-hidden /> Assign owner
            </Button>
          )
        }
      >
        <ListPage<Member>
          id="admin-team-members"
          caption="Members"
          columns={columns}
          data={members.data ?? []}
          getRowId={(m) => m.user.id}
          rowLabel={(m) => m.user.displayName || m.user.email}
          search={{ label: "Search members", placeholder: "Name or email" }}
          loading={members.isLoading}
          error={members.error}
          onRetry={() => void members.refetch()}
          empty={{ icon: <UserRound />, title: "No members yet.", description: "Invited people appear under Open invites until they sign in." }}
        />
        {assigning && <AssignOwnerDialog team={team} onClose={() => setAssigning(false)} />}
      </Card>
      {/* Platform admins revoke any invite, as an owner would; auditors see them. */}
      <InviteList team={team} myRole={isAdmin ? "owner" : undefined} />
    </Stack>
  );
}

/** Signed-in users matching the typed text, plus "Invite <address>" when it's a new email. */
function useOwnerOptions(text: string): ComboboxOption[] {
  const q = useDebounced(text.trim(), 250);
  const users = useQuery({
    queryKey: ["admin", "users", "owner-picker", q],
    queryFn: async () => unwrap(await api.GET("/v1/admin/users", { params: { query: { q: q || undefined, limit: 20, status: "active" } } })),
  });
  const list: ComboboxOption[] = (users.data?.items ?? []).map((u) => ({ value: u.email, label: u.displayName || u.email, hint: u.displayName ? u.email : undefined }));
  const typed = text.trim().toLowerCase();
  if (looksLikeEmail(typed) && !list.some((o) => o.value.toLowerCase() === typed)) list.push({ value: typed, label: `Invite ${typed}`, hint: "New person" });
  return list;
}

function AssignOwnerDialog({ team, onClose }: { team: string; onClose: () => void }) {
  const qc = useQueryClient();
  const [text, setText] = useState("");
  const [email, setEmail] = useState<string | null>(null);
  const [chosen, setChosen] = useState<ComboboxOption | null>(null);
  const options = useOwnerOptions(text);
  const items = chosen && !options.some((o) => o.value === chosen.value) ? [chosen, ...options] : options;
  const assign = useMutation({
    mutationFn: async () => unwrap(await api.POST("/v1/admin/teams/{team}/owners", { params: { path: { team } }, body: { email: email! } })),
    onSuccess: (r) => {
      toast.success(r.status === "invited" ? "Owner invited" : "Owner assigned", email ?? undefined);
      void qc.invalidateQueries({ queryKey: ["admin"] });
      void qc.invalidateQueries({ queryKey: ["team", team] });
      onClose();
    },
  });
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title="Assign an owner"
      description="Promotes a member, adds someone who has signed in, or invites a new email address."
      footer={
        <>
          <DialogClose>Cancel</DialogClose>
          <Button loading={assign.isPending} disabled={!email} onClick={() => assign.mutate()}>
            Assign owner
          </Button>
        </>
      }
    >
      <Form onSubmit={(e) => e.preventDefault()}>
        <Field label="New owner" description="Search by name or email, or type a new email address.">
          <Combobox
            items={items}
            value={email}
            onValueChange={(v, option) => {
              setEmail(v);
              setChosen(option);
            }}
            onInputValueChange={setText}
            placeholder="Name or email"
            emptyText="Type a full email address to invite someone new."
            autoHighlight
          />
        </Field>
        <ErrorAlert error={assign.error} />
      </Form>
    </Dialog>
  );
}
