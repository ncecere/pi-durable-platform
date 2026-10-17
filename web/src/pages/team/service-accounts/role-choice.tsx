/* The service account's role (member or editor), with what each reaches and what the role is for. */
import { RadioGroup } from "@/components/ui/radio-group/radio-group";
import { roleExplanation, roleLabel, serviceAccountRoleHelp, serviceAccountRoles, type ServiceAccountRole } from "./labels";

export function RoleChoice({ value, onChange, disabled }: { value: ServiceAccountRole; onChange: (r: ServiceAccountRole) => void; disabled?: boolean }) {
  return (
    <RadioGroup<ServiceAccountRole>
      legend="Role"
      description={roleExplanation}
      value={value}
      onValueChange={onChange}
      disabled={disabled}
      options={serviceAccountRoles.map((r) => ({ value: r, label: roleLabel(r), description: serviceAccountRoleHelp[r] }))}
    />
  );
}
