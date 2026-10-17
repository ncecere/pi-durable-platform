/* A confirmation dialog for a mutation on one chosen item (delete, revoke, remove…). */
import { AlertDialog, type AlertDialogProps } from "@/components/ui/dialog/dialog";

type ConfirmMutationDialogProps<T> = Omit<AlertDialogProps, "open" | "onOpenChange" | "busy" | "error" | "onConfirm"> & {
  /** The item being confirmed; the dialog is open while it is not null. */
  target: T | null;
  /** Clears the target. */
  onClose: () => void;
  /** Supplies busy / error, and is reset when the dialog closes. */
  mutation: { isPending: boolean; error: unknown; reset: () => void };
  onConfirm: (target: T) => void;
};

/**
 * An AlertDialog bound to a target and a mutation: open while `target` is
 * set, busy while the mutation runs, shows its error, and resets it on close.
 */
export function ConfirmMutationDialog<T>({ target, onClose, mutation, onConfirm, ...props }: ConfirmMutationDialogProps<T>) {
  return (
    <AlertDialog
      open={target !== null}
      onOpenChange={(o) => {
        if (!o) {
          onClose();
          mutation.reset();
        }
      }}
      busy={mutation.isPending}
      error={mutation.error}
      onConfirm={() => target && onConfirm(target)}
      {...props}
    />
  );
}
