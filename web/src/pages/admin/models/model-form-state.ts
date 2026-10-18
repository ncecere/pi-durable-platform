/* The model form's state: numbers as typed text, checked on submit, and the create and update bodies. */
import type { Schemas } from "@/api/client";
import type { Model } from "@/api/models";

export type ModelForm = {
  upstreamId: string;
  name: string;
  contextWindow: string;
  maxTokens: string;
  images: boolean;
  reasoning: boolean;
  inputPrice: string;
  outputPrice: string;
  enabled: boolean;
  visibility: Model["visibility"];
  /** Team slugs (visibility "teams"). */
  teams: string[];
};

/** The API's defaults for a new model. */
export const newModelForm: ModelForm = {
  upstreamId: "",
  name: "",
  contextWindow: "128000",
  maxTokens: "16000",
  images: false,
  reasoning: false,
  inputPrice: "",
  outputPrice: "",
  enabled: true,
  visibility: "all",
  teams: [],
};

const text = (n: number | null | undefined) => (n == null ? "" : String(n));

export const formOf = (m: Model): ModelForm => ({
  upstreamId: m.upstreamId,
  name: m.name,
  contextWindow: text(m.contextWindow),
  maxTokens: text(m.maxTokens),
  images: m.images,
  reasoning: m.reasoning,
  inputPrice: text(m.inputPrice),
  outputPrice: text(m.outputPrice),
  enabled: m.enabled,
  visibility: m.visibility,
  teams: (m.teams ?? []).map((t) => t.slug),
});

const count = (v: string) => (/^\d+$/.test(v.trim()) && Number(v) > 0 ? undefined : "Enter a whole number of tokens.");
const amount = (v: string) => (!v.trim() || (Number.isFinite(Number(v)) && Number(v) >= 0) ? undefined : "Enter an amount in dollars, such as 2.50, or leave it empty.");

export type ModelProblems = Partial<Record<keyof ModelForm, string>>;

export function problemsOf(f: ModelForm, isNew: boolean): ModelProblems {
  return {
    upstreamId: isNew && !f.upstreamId.trim() ? "Enter the provider's ID for the model." : undefined,
    name: !isNew && !f.name.trim() ? "Enter a name." : undefined,
    contextWindow: count(f.contextWindow),
    maxTokens: count(f.maxTokens) ?? (Number(f.maxTokens) > Number(f.contextWindow) ? "At most the context window." : undefined),
    inputPrice: amount(f.inputPrice),
    outputPrice: amount(f.outputPrice),
    teams: f.visibility === "teams" && f.teams.length === 0 ? "Choose at least one team, or publish it to all teams." : undefined,
  };
}

const common = (f: ModelForm) => ({
  contextWindow: Number(f.contextWindow),
  maxTokens: Number(f.maxTokens),
  images: f.images,
  reasoning: f.reasoning,
  enabled: f.enabled,
  visibility: f.visibility,
  teams: f.visibility === "teams" ? f.teams : undefined,
});

/** A new model: empty prices are left out; the name defaults to the model ID. */
export function bodyOf(f: ModelForm): Schemas["ModelCreate"] {
  return {
    upstreamId: f.upstreamId.trim(),
    name: f.name.trim() || undefined,
    ...common(f),
    inputPrice: f.inputPrice.trim() ? Number(f.inputPrice) : undefined,
    outputPrice: f.outputPrice.trim() ? Number(f.outputPrice) : undefined,
  };
}

/** A change: an emptied price is cleared (a negative value), an empty one left as it is. */
export function updateOf(f: ModelForm, m: Model): Schemas["ModelUpdate"] {
  const priceOf = (v: string, was: number | null | undefined) => (v.trim() ? Number(v) : was != null ? -1 : undefined);
  return {
    name: f.name.trim(),
    ...common(f),
    inputPrice: priceOf(f.inputPrice, m.inputPrice),
    outputPrice: priceOf(f.outputPrice, m.outputPrice),
  };
}
