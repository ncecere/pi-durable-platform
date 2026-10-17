// Loading: defaults, then the YAML file, then the environment, with
// secrets optionally read from *_FILE files.

package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads PDP_CONFIG_FILE (if set) and the process environment.
func Load() (Config, error) {
	return LoadFrom(os.Getenv("PDP_CONFIG_FILE"), os.LookupEnv)
}

// LoadFrom resolves configuration from an optional YAML file and an
// environment lookup function. It performs no network calls.
func LoadFrom(path string, lookup func(string) (string, bool)) (Config, error) {
	c := Defaults()
	file := map[string]string{}
	if path != "" {
		var err error
		if file, err = readFile(path); err != nil {
			return Config{}, err
		}
	}
	var errs []error
	for _, s := range settings {
		value, ok, err := resolve(s, file, lookup)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !ok {
			continue
		}
		if err := s.apply(&c, value); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", s.key, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	c.AppURL = strings.TrimRight(c.AppURL, "/")
	return c, nil
}

// resolve finds a setting's value: the file, then its *_file, then the
// environment, then its *_FILE (later sources win).
func resolve(s setting, file map[string]string, lookup func(string) (string, bool)) (string, bool, error) {
	yamlKey := strings.ToLower(s.key)
	value, ok := "", false
	if v, found := file[yamlKey]; found {
		value, ok = v, true
	}
	if s.secret {
		if p, found := file[yamlKey+"_file"]; found {
			v, err := readSecretFile(p)
			if err != nil {
				return "", false, fmt.Errorf("%s_file: %w", yamlKey, err)
			}
			value, ok = v, true
		}
	}
	if v, found := lookup(s.key); found {
		value, ok = v, true
	}
	if s.secret {
		if p, found := lookup(s.key + "_FILE"); found && p != "" {
			v, err := readSecretFile(p)
			if err != nil {
				return "", false, fmt.Errorf("%s_FILE: %w", s.key, err)
			}
			value, ok = v, true
		}
	}
	return value, ok, nil
}

func readFile(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config file: %w", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("config file: %w", err)
	}
	known := map[string]bool{}
	for _, s := range settings {
		known[strings.ToLower(s.key)] = true
		if s.secret {
			known[strings.ToLower(s.key)+"_file"] = true
		}
	}
	out := make(map[string]string, len(doc))
	for k, v := range doc {
		if !known[k] {
			return nil, fmt.Errorf("config file: unknown key %q", k)
		}
		switch t := v.(type) {
		case []any:
			parts := make([]string, 0, len(t))
			for _, item := range t {
				parts = append(parts, fmt.Sprint(item))
			}
			out[k] = strings.Join(parts, ",")
		case nil:
			out[k] = ""
		default:
			out[k] = fmt.Sprint(t)
		}
	}
	return out, nil
}

func readSecretFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
