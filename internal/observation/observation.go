/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package observation defines what a probe runner reports to the incident
// engine. It is shared by both binaries and deliberately holds no logic.
package observation

import "time"

// Kinds of signal a runner can report.
const (
	// KindFailure is a check that failed its own assertions.
	KindFailure = "failure"

	// KindRecovery is a check that passed after previously failing. The engine
	// uses it to close incidents.
	KindRecovery = "recovery"

	// KindBodyDrift is a check that PASSED while its response body moved away
	// from the learned baseline.
	KindBodyDrift = "bodyDrift"

	// KindLatencyShift is a check that PASSED while getting materially slower.
	KindLatencyShift = "latencyShift"
)

// Observation is one signal about one probe.
//
// What is deliberately absent is as important as what is here:
//
//   - No response body, and no unredacted text. Bodies are embedded inside the
//     probe runner and never leave that pod; only normalized, redacted text
//     travels.
//   - No policy, no credentials, no action configuration. The incident engine
//     mounts the same probe ConfigMap and auth Secret as the runners, so it
//     already knows every probe's policy. Shipping that per observation would
//     put credentials on the wire for no reason.
type Observation struct {
	// Probe is "namespace/name", matching the canary it came from.
	Probe string `json:"probe"`

	// Kind is one of the Kind constants above.
	Kind string `json:"kind"`

	// Text is the normalized, redacted document the model compares. For a
	// failure this is the failure text; for drift it is empty, because the
	// body it was derived from must not leave the runner.
	Text string `json:"text,omitempty"`

	// Labels are the canary's metadata labels, used to evaluate a policy's
	// correlation candidateSelector.
	Labels map[string]string `json:"labels,omitempty"`

	ProbeType      string `json:"probeType,omitempty"`
	URL            string `json:"url,omitempty"`
	StatusCode     int    `json:"statusCode,omitempty"`
	ExpectedStatus int    `json:"expectedStatus,omitempty"`

	// Message is the human-readable check message, already redacted. It is
	// shown to operators and included in the language model's prompt.
	Message string `json:"message,omitempty"`

	// DriftScore and LatencyZScore are computed in the runner, which is the
	// only place with access to the raw material.
	DriftScore    float64 `json:"driftScore,omitempty"`
	LatencyZScore float64 `json:"latencyZScore,omitempty"`

	At time.Time `json:"at"`
}

// Batch is what a probe runner POSTs to the incident engine.
//
// Shard identifies the sending replica so the engine can tell "this shard has
// no results for probe X" apart from "probe X is gone".
type Batch struct {
	Shard        string        `json:"shard"`
	Observations []Observation `json:"observations,omitempty"`
}
