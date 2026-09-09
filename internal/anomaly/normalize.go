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

// Package anomaly turns check outcomes into stable text, learns what "normal"
// looks like per probe, and scores how far a new outcome sits from it.
package anomaly

import (
	"fmt"
	"regexp"
	"strings"
)

// Masking is the load-bearing step in this whole feature.
//
// Real failure text and real response bodies are full of tokens that change on
// every single request: timestamps, request IDs, UUIDs, ephemeral ports, byte
// counts. Left alone, every observation is unique, so novelty clustering
// reports everything as new and correlation never finds two failures similar
// enough to merge. Masking those tokens is what makes "have we seen this
// before?" answerable at all.
//
// Order matters. Each pattern is applied in sequence, so the more specific ones
// must run first — a UUID is also a run of hex, and a timestamp is also a run
// of digits.
var maskPatterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	// RFC3339 / ISO8601 and common log timestamps.
	{regexp.MustCompile(
		`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?`), "<ts>"},
	// UUIDs — before the generic hex rule, which would otherwise eat the parts.
	{regexp.MustCompile(
		`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`), "<uuid>"},
	// IPv4 with an optional port, which covers most dial errors.
	{regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(?::\d{1,5})?\b`), "<ip>"},
	// Go-style durations: 1.5s, 250ms, 3m0s.
	{regexp.MustCompile(`\b\d+(?:\.\d+)?(?:ns|µs|us|ms|s|m|h)\b`), "<dur>"},
	// Long hex runs: request IDs, trace IDs, content hashes.
	{regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b`), "<hex>"},
	// Anything numeric that survived.
	{regexp.MustCompile(`\b\d+(?:\.\d+)?\b`), "<num>"},
}

var whitespacePattern = regexp.MustCompile(`\s+`)

// Normalizer converts raw text into a stable, low-cardinality form.
// The zero value is not usable; build one with NewNormalizer.
type Normalizer struct {
	// redact is applied before any masking, so user-supplied secrets never
	// reach the model, the cache key, or a log line.
	redact []*regexp.Regexp
}

// NewNormalizer compiles the policy's redaction patterns.
// An invalid pattern is a configuration error and is reported, not ignored:
// silently dropping a redaction rule would leak exactly what it was meant to
// protect.
func NewNormalizer(redact []string) (*Normalizer, error) {
	normalizer := &Normalizer{}

	for _, raw := range redact {
		compiled, err := regexp.Compile(raw)
		if err != nil {
			return nil, fmt.Errorf("compiling redact pattern %q: %w", raw, err)
		}
		normalizer.redact = append(normalizer.redact, compiled)
	}

	return normalizer, nil
}

// Normalize redacts, masks, lowercases, and collapses whitespace.
func (n *Normalizer) Normalize(text string) string {
	for _, pattern := range n.redact {
		text = pattern.ReplaceAllString(text, "<redacted>")
	}

	for _, mask := range maskPatterns {
		text = mask.pattern.ReplaceAllString(text, mask.replacement)
	}

	return strings.TrimSpace(whitespacePattern.ReplaceAllString(strings.ToLower(text), " "))
}

// Failure describes one failed check, in the form the model consumes.
type Failure struct {
	ProbeType      string
	StatusCode     int
	ExpectedStatus int
	Message        string
}

// FailureText renders a failure as a single normalized document.
//
// The status code is deliberately kept as a literal rather than masked. It is
// low-cardinality (a few dozen values across all of HTTP) and highly
// meaningful: "everything is returning 503" and "everything is returning 404"
// are different incidents. Numbers inside the free-text message are still
// masked, because those are the ones that change on every request.
//
// The probe name is deliberately absent. Correlation asks whether two DIFFERENT
// probes failed for the same reason, so including the probe name would make
// every observation trivially unique and defeat the comparison.
func (n *Normalizer) FailureText(failure Failure) string {
	var builder strings.Builder

	fmt.Fprintf(&builder, "type=%s", failure.ProbeType)
	if failure.StatusCode > 0 {
		fmt.Fprintf(&builder, " status=%d", failure.StatusCode)
	}
	if failure.ExpectedStatus > 0 {
		fmt.Fprintf(&builder, " expected=%d", failure.ExpectedStatus)
	}
	fmt.Fprintf(&builder, " message=%s", n.Normalize(failure.Message))

	return builder.String()
}

// BodyText renders a response body for drift comparison.
//
// Only the body is included. Drift compares a probe's body against its own
// history, so status codes and probe names would be constant noise — and by the
// time drift runs, the check has already passed, so the status is whatever the
// canary asserted.
func (n *Normalizer) BodyText(body string, maxBytes int) string {
	if maxBytes > 0 && len(body) > maxBytes {
		body = body[:maxBytes]
	}

	return n.Normalize(body)
}
