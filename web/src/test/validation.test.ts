/* Email validation: name@localhost (the dev personas) is accepted; other single-label domains aren't. */
import { emailProblem, looksLikeEmail } from "../lib/validation";

describe("emailProblem", () => {
  it("accepts ordinary addresses and name@localhost", () => {
    for (const ok of ["una@example.com", "a.b+c@sub.example.org", "admin@localhost", "Alex@LOCALHOST", "  owner@localhost  "]) expect(emailProblem(ok), ok).toBeUndefined();
  });

  it("rejects other single-label domains and malformed text", () => {
    for (const bad of ["una@example", "una@intranet", "una", "@example.com", "una@", "una@@example.com", "u na@example.com", "una@example.", "una@.com"]) {
      expect(looksLikeEmail(bad), bad).toBe(false);
      expect(emailProblem(bad), bad).toBe("Enter an email address like name@example.org.");
    }
    expect(emailProblem("  ")).toBe("Enter an email address.");
  });
});
