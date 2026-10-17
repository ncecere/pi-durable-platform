/* An audit entry's before and after as a table of plain field names and values ("Role | Member | Editor"). */
import { Table, Td, Tr } from "@/components/ui/table/table";
import s from "../../pages/shared.module.css";
import { changedRows, valueText } from "./changes";

export function AuditChangesTable({ label, before, after }: { label: string; before: object; after: object }) {
  const rows = changedRows({ before, after });
  if (rows.length === 0) return <p className={s.muted}>No fields changed.</p>;
  return (
    <Table caption={label} columns={["Field", "Before", "After"]} density="compact">
      {rows.map((r) => (
        <Tr key={r.field}>
          <Td>{r.field}</Td>
          <Td muted={r.before === undefined}>{valueText(r.before)}</Td>
          <Td muted={r.after === undefined}>{valueText(r.after)}</Td>
        </Tr>
      ))}
    </Table>
  );
}
