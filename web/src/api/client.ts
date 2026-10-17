import createClient, { type Middleware } from "openapi-fetch";
import type { components, paths } from "./schema.gen";

export type Schemas = components["schemas"];

/** An error response from the API: {"error": {"code", "message"}}. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    /** Structured context for some errors. */
    readonly details?: Record<string, unknown>,
    /** Seconds from the Retry-After header (429 rate_limited). */
    readonly retryAfter?: number,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

// The session's CSRF token, from GET /v1/me. Sent on every unsafe request.
let csrfToken = "";
export function setCsrfToken(token: string) {
  csrfToken = token;
}

/** The CSRF header for unsafe requests made without the API client. */
export function csrfHeader(): Record<string, string> {
  return csrfToken ? { "X-CSRF-Token": csrfToken } : {};
}

const csrf: Middleware = {
  onRequest({ request }) {
    if (!["GET", "HEAD", "OPTIONS"].includes(request.method) && csrfToken) {
      request.headers.set("X-CSRF-Token", csrfToken);
    }
    return request;
  },
};

// Absolute base (same origin) so requests also construct outside a browser.
export const api = createClient<paths>({
  baseUrl: globalThis.location?.origin ?? "",
  credentials: "same-origin",
  // Look fetch up per call (not at import) so it can be replaced in tests.
  fetch: (request) => globalThis.fetch(request),
});
api.use(csrf);

type Result<T> = { data?: { data: T }; error?: unknown; response: Response };

/**
 * Returns the `data` of a successful response or throws an ApiError. Use it
 * to wrap every call: `unwrap(await api.GET("/v1/me"))`.
 */
export function unwrap<T>(res: Result<T>): T {
  if (res.data !== undefined) return res.data.data;
  const err = (res.error ?? {}) as { error?: { code?: string; message?: string; details?: Record<string, unknown> } };
  const retry = Number(res.response.headers?.get("Retry-After"));
  // A save conflict: the app keeps the person's edits and loads the latest version, so the server's
  // "Reload and try again" (meant for API clients) would be wrong advice here.
  const message = res.response.status === 412 ? staleMessage : err.error?.message;
  throw new ApiError(
    res.response.status,
    err.error?.code ?? "http_" + res.response.status,
    message ?? `Request failed (${res.response.status})`,
    err.error?.details,
    Number.isFinite(retry) && retry > 0 ? retry : undefined,
  );
}

/** A 412's message in the app (see unwrap). */
export const staleMessage = "Someone else changed this while you were editing, so your changes weren't saved. They're still here.";

/** Formats a revision for the If-Match header. */
export const ifMatch = (revision: number) => ({ "If-Match": `"${revision}"` });

export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  if (err instanceof Error) return err.message;
  return "Something went wrong";
}
