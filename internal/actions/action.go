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

// Package actions performs what a policy asks for when an incident opens:
// escalate to a language model, notify a channel, ship a record to a logging
// backend, or just move a metric.
package actions

import (
	"context"
	"net/http"
	"time"

	"github.com/bryanbarton525/pulse/internal/incident"
)

// Action types, matching AnomalyPolicy.spec.actions[].type.
const (
	TypeMetric        = "metric"
	TypeLLM           = "llm"
	TypeSlack         = "slack"
	TypeObservability = "observability"
)

// Action is one thing to do about an incident.
//
// Fire returns a result string that later actions can consume — this is how a
// slack action includes the investigation an llm action produced a moment
// earlier, without either knowing about the other.
type Action interface {
	Name() string
	Type() string
	Fire(ctx context.Context, current *incident.Incident) (string, error)
}

// Credentials resolves credential IDs against the mounted auth Secret. Actions
// never see Kubernetes objects; the engine hands them a lookup.
type Credentials interface {
	Lookup(id string) string
}

// CredentialMap is the straightforward Credentials implementation.
type CredentialMap map[string]string

// Lookup implements Credentials.
func (c CredentialMap) Lookup(id string) string { return c[id] }

// defaultClient is shared by every HTTP-based action. Actions run off the probe
// path entirely, so a slow notification cannot delay a check.
func defaultClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout}
}
