/* The instance identity: names, theme, logo and help link come from GET /v1/auth/config; the default is "Pi Durable Platform". */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { axe } from "vitest-axe";
import type { Schemas } from "../api/client";
import { applyInstance, defaultInstance, instanceOf, tagline, type Instance } from "../lib/instance";
import { SignInPage } from "../session";
import { authConfig, meFor } from "./fixtures";
import { mockApi, renderApp } from "./harness";

const acme: Instance = {
  name: "Acme Tools Hub",
  orgName: "Acme Corp",
  theme: "neutral",
  logoUrl: "/brand/logo.svg",
  supportUrl: "https://help.example.com/mcp",
};

const sso = (instance: Instance, oidcEnabled = true): Schemas["AuthConfig"] => authConfig({ instance, oidcEnabled, devAuthEnabled: false, devAccounts: [] });

function withQuery(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}

const shell = (instance: Instance) => ({ "GET /v1/auth/config": () => sso(instance), "GET /v1/me": () => meFor("none", null) });

afterEach(() => {
  document.documentElement.dataset.brand = "neutral";
  document.title = "";
});

describe("instance helpers", () => {
  it("fills in defaults and builds the tagline", () => {
    expect(defaultInstance.name).toBe("Pi Durable Platform");
    expect(instanceOf(undefined)).toEqual(defaultInstance);
    expect(instanceOf({ instance: { name: "  ", orgName: "", theme: "neutral", logoUrl: "", supportUrl: null } })).toEqual(defaultInstance);
    expect(tagline(defaultInstance)).toBe("Durable agents for your teams");
    expect(tagline(acme)).toBe("Durable agents for Acme Corp teams");
  });

  it("applies the theme and title to the document", () => {
    delete document.documentElement.dataset.brand;
    applyInstance(acme);
    expect(document.documentElement).toHaveAttribute("data-brand", "neutral");
    expect(document.title).toBe("Acme Tools Hub");
    // A page title the shell set ("Page · Instance") is kept.
    document.title = "Home \u00b7 Acme Tools Hub";
    applyInstance(acme);
    expect(document.title).toBe("Home \u00b7 Acme Tools Hub");
  });
});

describe("sign-in page", () => {
  it("is neutral by default: product name as the wordmark beside the product mark, no organisation, no instance logo and no help link", async () => {
    mockApi({ "GET /v1/auth/config": () => sso(defaultInstance) });
    const { container } = render(withQuery(<SignInPage />));
    expect(await screen.findByText("Use your organization account to continue.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 1, name: "Pi Durable Platform" })).toBeInTheDocument();
    expect(screen.getByText("Durable agents for your teams")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Sign in with single sign-on/ })).toHaveAttribute("href", expect.stringMatching(/^\/auth\/login\?next=/));
    // No instance logo: the product's Wake mark, beside the product's name as its wordmark.
    expect(container.querySelector("img")).toHaveAttribute("src", "/pi-durable-platform-mark-small.svg");
    expect(container.querySelector("img")).toHaveAttribute("alt", "");
    expect(screen.queryByRole("link", { name: /Help/ })).toBeNull();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("uses the configured names, logo and help link", async () => {
    mockApi({ "GET /v1/auth/config": () => sso(acme) });
    const { container } = render(withQuery(<SignInPage />));
    expect(await screen.findByRole("heading", { level: 1, name: "Acme Tools Hub" })).toBeInTheDocument();
    expect(screen.getByText("Durable agents for Acme Corp teams")).toBeInTheDocument();
    expect(screen.getByText("Use your Acme Corp account to continue.")).toBeInTheDocument();
    expect(container.querySelector("img")).toHaveAttribute("src", "/brand/logo.svg");
    expect(screen.getByRole("link", { name: /Help signing in/ })).toHaveAttribute("href", "https://help.example.com/mcp");
    expect(await axe(container)).toHaveNoViolations();
  });

  it("says when no sign-in method is configured", async () => {
    mockApi({ "GET /v1/auth/config": () => sso(defaultInstance, false) });
    render(withQuery(<SignInPage />));
    expect(await screen.findByText("No sign-in method is configured.")).toBeInTheDocument();
  });
});

describe("app shell", () => {
  it("shows the instance name and logo, applies the theme and offers Help in the user menu", async () => {
    const user = userEvent.setup();
    delete document.documentElement.dataset.brand;
    mockApi(shell(acme));
    const { container } = renderApp("/");
    const brand = await screen.findByRole("link", { name: "Acme Tools Hub" });
    expect(brand.querySelector("img")).toHaveAttribute("src", "/brand/logo.svg");
    await waitFor(() => expect(document.documentElement).toHaveAttribute("data-brand", "neutral"));
    await waitFor(() => expect(document.title).toBe("Home · Acme Tools Hub"));

    await user.click(screen.getByRole("button", { name: /Account:\s*Una User/ }));
    const menu = await screen.findByRole("menu");
    expect(within(menu).getByRole("menuitem", { name: "Help" })).toHaveAttribute("href", "https://help.example.com/mcp");
    await user.keyboard("{Escape}");
    expect(await axe(container)).toHaveNoViolations();
  });

  it("has no Help link without SUPPORT_URL, and the not-found page offers Help with it", async () => {
    const user = userEvent.setup();
    mockApi(shell(defaultInstance));
    const { unmount } = renderApp("/nowhere");
    expect(await screen.findByRole("heading", { level: 1, name: "Page not found" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Pi Durable Platform" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Help" })).toBeNull();
    await user.click(screen.getByRole("button", { name: /Account:\s*Una User/ }));
    expect(within(await screen.findByRole("menu")).queryByRole("menuitem", { name: "Help" })).toBeNull();
    unmount();

    mockApi(shell(acme));
    renderApp("/nowhere");
    expect(await screen.findByRole("heading", { level: 1, name: "Page not found" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Help" })).toHaveAttribute("href", "https://help.example.com/mcp");
  });
});
