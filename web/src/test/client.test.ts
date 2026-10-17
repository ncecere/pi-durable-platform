/* The API client: unwrap's errors, the CSRF header on writes, If-Match, and the retry rules. */
import { ApiError, api, csrfHeader, errorMessage, ifMatch, setCsrfToken, staleMessage, unwrap } from "../api/client";
import { isRateLimited, retryDelay, shouldRetry, shouldRetrySession } from "../lib/retry";
import { mockApi, Reply } from "./harness";

afterEach(() => setCsrfToken(""));

describe("unwrap", () => {
  it("returns the data of a success", async () => {
    mockApi({ "GET /healthz": () => ({ status: "ok", checks: {} }) });
    expect(unwrap(await api.GET("/healthz"))).toEqual({ status: "ok", checks: {} });
  });

  it("throws an ApiError with the status, code, message, details and Retry-After", async () => {
    mockApi({ "GET /v1/admin/users": () => Reply.error(429, "rate_limited", "Slow down.", { scope: "user" }, { "Retry-After": "12" }) });
    const err = await api.GET("/v1/admin/users").then(unwrap).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 429, code: "rate_limited", message: "Slow down.", details: { scope: "user" }, retryAfter: 12 });
    expect(errorMessage(err)).toBe("Slow down.");
  });

  it("explains a save conflict (412) in the app's words, and names an unknown failure by its status", async () => {
    mockApi({
      "PATCH /v1/admin/teams/x": () => Reply.error(412, "stale", "Reload and try again."),
      "GET /v1/admin/teams/y": () => new Response("oops", { status: 502 }),
    });
    const stale = await api.PATCH("/v1/admin/teams/{team}", { params: { path: { team: "x" }, header: ifMatch(3) }, body: { name: "X" } }).then(unwrap).catch((e: unknown) => e);
    expect(stale).toMatchObject({ status: 412, message: staleMessage });
    const bad = await api.GET("/v1/admin/teams/{team}", { params: { path: { team: "y" } } }).then(unwrap).catch((e: unknown) => e);
    expect(bad).toMatchObject({ status: 502, code: "http_502", message: "Request failed (502)" });
  });
});

describe("headers", () => {
  it("sends the session's CSRF token on writes only, and If-Match as a quoted revision", async () => {
    const calls = mockApi({ "GET /v1/me": () => null, "PATCH /v1/admin/teams/x": () => ({}) });
    setCsrfToken("tok");
    expect(csrfHeader()).toEqual({ "X-CSRF-Token": "tok" });
    await api.GET("/v1/me");
    await api.PATCH("/v1/admin/teams/{team}", { params: { path: { team: "x" }, header: ifMatch(7) }, body: { description: "d" } });
    expect(calls[0]!.headers.get("X-CSRF-Token")).toBeNull();
    expect(calls[1]!.headers.get("X-CSRF-Token")).toBe("tok");
    expect(calls[1]!.headers.get("If-Match")).toBe('"7"');
    setCsrfToken("");
    expect(csrfHeader()).toEqual({});
  });
});

describe("retrying", () => {
  const limited = (retryAfter?: number) => new ApiError(429, "rate_limited", "Too many requests.", undefined, retryAfter);

  it("waits for Retry-After (at most a minute), else backs off", () => {
    expect(isRateLimited(limited(12))).toBe(true);
    expect(isRateLimited(limited(3600))).toBe(false);
    expect(retryDelay(0, limited(12), () => 0)).toBe(12_000);
    expect(retryDelay(3, limited(), () => 0)).toBe(8_000);
    expect(retryDelay(2, new Error("network"))).toBe(4_000);
  });

  it("retries queries twice, never a client error, and the session until a rate limit lifts", () => {
    expect(shouldRetry(1, limited(5))).toBe(true);
    expect(shouldRetry(2, limited(5))).toBe(false);
    expect(shouldRetry(0, new ApiError(404, "not_found", "x"))).toBe(false);
    expect(shouldRetry(0, new ApiError(503, "unavailable", "x"))).toBe(true);
    expect(shouldRetrySession(10, limited(5))).toBe(true);
  });
});
