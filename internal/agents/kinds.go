package agents

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
)

// Agent kinds (docs/first-party-agents.md): a template version says which of
// the runtime's compositions its agents run (AGENT_KIND) and that kind's
// settings (AGENT_KIND_SETTINGS). The runtime checks them again
// (runtime/src/kinds); the platform refuses what the runtime would.

// The kinds a template version may have.
const (
	KindAssistant  = "assistant"
	KindTriage     = "triage"
	KindResearcher = "researcher"
)

// Bounds of a kind's settings (as runtime/src/kinds/settings.ts has them).
const (
	maxKindTools    = 50
	maxPlanSteps    = 20
	defaultPlanSize = 8
)

var (
	errBadKind = apperr.Invalid("invalid_kind", "kind must be assistant, triage or researcher")
)

func errBadKindSettings(msg string) error {
	return apperr.Invalid("invalid_kind_settings", msg)
}

// KindSettings are a kind's settings as sent: each field belongs to one kind,
// and a nil field takes its default. Triage: ListTools, ReadTools,
// WriteTools, ApproveWrites. Researcher: BackgroundSubagents, MaxPlanSteps.
type KindSettings struct {
	ListTools           *[]string `json:"listTools,omitempty"`
	ReadTools           *[]string `json:"readTools,omitempty"`
	WriteTools          *[]string `json:"writeTools,omitempty"`
	ApproveWrites       *bool     `json:"approveWrites,omitempty"`
	BackgroundSubagents *bool     `json:"backgroundSubagents,omitempty"`
	MaxPlanSteps        *int32    `json:"maxPlanSteps,omitempty"`
}

// triageSettings and researcherSettings are what is stored (and sent to the
// runtime): every field, with its default.
type triageSettings struct {
	ListTools     []string `json:"listTools"`
	ReadTools     []string `json:"readTools"`
	WriteTools    []string `json:"writeTools"`
	ApproveWrites bool     `json:"approveWrites"`
}

type researcherSettings struct {
	BackgroundSubagents bool  `json:"backgroundSubagents"`
	MaxPlanSteps        int32 `json:"maxPlanSteps"`
}

// checkKind returns a kind and its settings, checked, as stored: the
// settings with their defaults, as a JSON object. kind "" is the assistant.
func checkKind(kind string, in *KindSettings) (string, []byte, error) {
	if in == nil {
		in = &KindSettings{}
	}
	triageField := in.ListTools != nil || in.ReadTools != nil || in.WriteTools != nil || in.ApproveWrites != nil
	researcherField := in.BackgroundSubagents != nil || in.MaxPlanSteps != nil
	switch kind {
	case "", KindAssistant:
		if triageField || researcherField {
			return "", nil, errBadKindSettings("the assistant kind takes no settings")
		}
		return KindAssistant, []byte("{}"), nil
	case KindTriage:
		if researcherField {
			return "", nil, errBadKindSettings("triage takes listTools, readTools, writeTools and approveWrites")
		}
		s := triageSettings{ApproveWrites: true}
		var err error
		if s.ListTools, err = kindTools("listTools", in.ListTools); err != nil {
			return "", nil, err
		}
		if s.ReadTools, err = kindTools("readTools", in.ReadTools); err != nil {
			return "", nil, err
		}
		if s.WriteTools, err = kindTools("writeTools", in.WriteTools); err != nil {
			return "", nil, err
		}
		if in.ApproveWrites != nil {
			s.ApproveWrites = *in.ApproveWrites
		}
		b, err := json.Marshal(s)
		return KindTriage, b, err
	case KindResearcher:
		if triageField {
			return "", nil, errBadKindSettings("the researcher takes backgroundSubagents and maxPlanSteps")
		}
		s := researcherSettings{BackgroundSubagents: true, MaxPlanSteps: defaultPlanSize}
		if in.BackgroundSubagents != nil {
			s.BackgroundSubagents = *in.BackgroundSubagents
		}
		if in.MaxPlanSteps != nil {
			if *in.MaxPlanSteps < 1 || *in.MaxPlanSteps > maxPlanSteps {
				return "", nil, errBadKindSettings("maxPlanSteps is 1 to 20")
			}
			s.MaxPlanSteps = *in.MaxPlanSteps
		}
		b, err := json.Marshal(s)
		return KindResearcher, b, err
	default:
		return "", nil, errBadKind
	}
}

// kindTools checks one list of tool names or patterns: trimmed, without
// duplicates, at most maxKindTools.
func kindTools(field string, in *[]string) ([]string, error) {
	out := []string{}
	if in == nil {
		return out, nil
	}
	seen := map[string]bool{}
	for _, t := range *in {
		t = strings.TrimSpace(t)
		if !toolPatternRE.MatchString(t) {
			return nil, errBadKindSettings(field + " are tool names or patterns of 1-200 letters, digits, _ . - or *")
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) > maxKindTools {
		return nil, errBadKindSettings(field + " has at most 50 tools")
	}
	return out, nil
}

// KindSettingsOf reads stored settings (a version's kind_settings) for the
// API; fields of other kinds stay nil.
func KindSettingsOf(raw []byte) KindSettings {
	var out KindSettings
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

// kindApprovals is a version's approval tools with what its kind adds: a
// triage version's write tools ask first unless its settings turn that off.
// The version's own list comes first; the runtime reads at most 100.
func kindApprovals(kind string, raw []byte, tools []string) []string {
	if kind != KindTriage {
		return tools
	}
	var s triageSettings
	s.ApproveWrites = true
	if json.Unmarshal(raw, &s) != nil || !s.ApproveWrites || len(s.WriteTools) == 0 {
		return tools
	}
	out := append([]string{}, tools...)
	seen := map[string]bool{}
	for _, t := range out {
		seen[t] = true
	}
	for _, t := range s.WriteTools {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}

// kindEnv adds the runtime's AGENT_KIND and AGENT_KIND_SETTINGS (only for a
// kind other than the assistant, so an assistant's variables are as before).
func kindEnv(kind string, raw []byte, env map[string]string) {
	if kind == "" || kind == KindAssistant {
		return
	}
	env["AGENT_KIND"] = kind
	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		compact.Reset()
		compact.WriteString("{}")
	}
	env["AGENT_KIND_SETTINGS"] = compact.String()
}
