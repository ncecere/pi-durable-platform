/* Field validation messages shared by forms (F-05: say what's wrong, not only a red border). */

/**
 * Whether text looks like an email address: a local part, "@" and a domain
 * with a dot (name@example.org). A single-label domain is only accepted when
 * it is `localhost` (the development personas sign in as name@localhost).
 */
export function looksLikeEmail(email: string) {
  const v = email.trim();
  const at = v.lastIndexOf("@");
  if (at <= 0 || /\s/.test(v) || v.indexOf("@") !== at) return false;
  const domain = v.slice(at + 1);
  if (domain.toLowerCase() === "localhost") return true;
  return /^[^\s@.]+(\.[^\s@.]+)+$/.test(domain);
}

/** A message for an email field, or undefined when it looks fine. */
export function emailProblem(email: string) {
  const v = email.trim();
  if (!v) return "Enter an email address.";
  if (!looksLikeEmail(v)) return "Enter an email address like name@example.org.";
  return undefined;
}
