package proberunner

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ProbeConfig is the top-level structure that gets serialized into a ConfigMap.
// The controller builds this from all HttpCanary CRs, writes it to a ConfigMap,
// and the probe runner reads it from the mounted file.
type ProbeConfig struct {
	Probes []Probe `yaml:"probes"`
}

// AuthStore is the Secret-backed runtime credential map consumed by the probe runner.
type AuthStore struct {
	Values map[string]string `yaml:"values"`
}

const (
	ProbeOutputPrometheus = "prometheus"
	ProbeOutputStdout     = "stdout"
)

// ProbeOutput defines one destination for probe execution telemetry.
type ProbeOutput struct {
	Type string `yaml:"type"`
}

// Probe represents a single HTTP check to execute.
// Each field maps 1:1 to an HttpCanary CR's spec fields.
//
// Name uses the format "namespace/name" so the controller can map results
// back to specific CRs when it queries the /results endpoint.
type Probe struct {
	Name           string            `yaml:"name"`
	URL            string            `yaml:"url"`
	Method         string            `yaml:"method,omitempty"`
	Headers        map[string]string `yaml:"headers,omitempty"`
	Auth           *ProbeAuth        `yaml:"auth,omitempty"`
	Body           string            `yaml:"body,omitempty"`
	Interval       int               `yaml:"interval"`
	ExpectedStatus int               `yaml:"expectedStatus"`
	ContainsText   string            `yaml:"containsText,omitempty"`
	MCP            *ProbeMCP         `yaml:"mcp,omitempty"`
	Journey        []ProbeStep       `yaml:"journey,omitempty"`
	Outputs        []ProbeOutput     `yaml:"outputs,omitempty"`
	ConfigError    string            `yaml:"configError,omitempty"`
}

// ProbeAuth defines a runtime auth strategy that references mounted credentials.
type ProbeAuth struct {
	Type                 string `yaml:"type"`
	UsernameCredentialID string `yaml:"usernameCredentialID,omitempty"`
	PasswordCredentialID string `yaml:"passwordCredentialID,omitempty"`
	TokenCredentialID    string `yaml:"tokenCredentialID,omitempty"`
	HeaderName           string `yaml:"headerName,omitempty"`
	ValueCredentialID    string `yaml:"valueCredentialID,omitempty"`
}

// ProbeMCP defines a runtime MCP initialize + tools/list probe.
type ProbeMCP struct {
	ProtocolVersion        string   `yaml:"protocolVersion,omitempty"`
	ClientName             string   `yaml:"clientName,omitempty"`
	ClientVersion          string   `yaml:"clientVersion,omitempty"`
	RequireToolsCapability bool     `yaml:"requireToolsCapability,omitempty"`
	MinToolCount           int      `yaml:"minToolCount,omitempty"`
	RequiredTools          []string `yaml:"requiredTools,omitempty"`
}

// ProbeStep represents one HTTP request in a scripted synthetic journey.
type ProbeStep struct {
	Name           string            `yaml:"name"`
	URL            string            `yaml:"url"`
	Method         string            `yaml:"method,omitempty"`
	Headers        map[string]string `yaml:"headers,omitempty"`
	Body           string            `yaml:"body,omitempty"`
	ExpectedStatus int               `yaml:"expectedStatus"`
	ContainsText   string            `yaml:"containsText,omitempty"`
}

// LoadConfigFromFile reads a YAML config file from disk (the mounted ConfigMap)
// and parses it into a ProbeConfig struct.
//
// This is called:
//   - Once at startup
//   - Again whenever the file changes (ConfigMap update triggers a volume remount)
func LoadConfigFromFile(path string) (*ProbeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %s: %w", path, err)
	}

	var config ProbeConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}

	return &config, nil
}

// LoadAuthStoreFromFile reads the mounted Secret file that contains probe credentials.
func LoadAuthStoreFromFile(path string) (*AuthStore, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading auth file %s: %w", path, err)
	}

	var store AuthStore
	if err := yaml.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("parsing auth file %s: %w", path, err)
	}
	if store.Values == nil {
		store.Values = map[string]string{}
	}

	return &store, nil
}
