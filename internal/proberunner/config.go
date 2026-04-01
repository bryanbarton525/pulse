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

// Probe represents a single HTTP check to execute.
// Each field maps 1:1 to an HttpCanary CR's spec fields.
//
// Name uses the format "namespace/name" so the controller can map results
// back to specific CRs when it queries the /results endpoint.
type Probe struct {
	Name           string `yaml:"name"`
	URL            string `yaml:"url"`
	Interval       int    `yaml:"interval"`
	ExpectedStatus int    `yaml:"expectedStatus"`
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
