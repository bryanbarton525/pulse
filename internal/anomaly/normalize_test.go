package anomaly

import (
	"strings"
	"testing"
)

func mustNormalizer(t *testing.T, redact ...string) *Normalizer {
	t.Helper()

	normalizer, err := NewNormalizer(redact)
	if err != nil {
		t.Fatalf("NewNormalizer() error = %v", err)
	}
	return normalizer
}

// The central property: two occurrences of the SAME failure, differing only in
// the tokens that change every request, must normalize to the same string.
// Without this, novelty reports every failure as new and correlation never
// merges anything.
func TestNormalizeCollapsesVolatileTokens(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		a    string
		b    string
	}{
		{
			name: "timestamps",
			a:    "2026-08-30T22:14:05Z FATAL connection lost",
			b:    "2026-08-31T04:59:41Z FATAL connection lost",
		},
		{
			name: "request ids",
			a:    "request 3f2504e0-4f89-11d3-9a0c-0305e82c3301 failed",
			b:    "request 8a1b2c3d-1111-2222-3333-444455556666 failed",
		},
		{
			name: "ephemeral ports and addresses",
			a:    "dial tcp 10.96.0.5:5432: i/o timeout",
			b:    "dial tcp 10.96.14.201:5432: i/o timeout",
		},
		{
			name: "durations",
			a:    "context deadline exceeded after 1.523s",
			b:    "context deadline exceeded after 970ms",
		},
		{
			name: "trace ids",
			a:    "trace=4bf92f3577b34da6a3ce929d0e0e4736 upstream unavailable",
			b:    "trace=00f067aa0ba902b7cc1ef7bd9be1b3a2 upstream unavailable",
		},
		{
			name: "byte counts",
			a:    "read 4096 bytes before EOF",
			b:    "read 81920 bytes before EOF",
		},
		{
			name: "whitespace and case",
			a:    "Upstream   Unavailable",
			b:    "upstream unavailable",
		},
	}

	normalizer := mustNormalizer(t)
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			gotA := normalizer.Normalize(testCase.a)
			gotB := normalizer.Normalize(testCase.b)
			if gotA != gotB {
				t.Fatalf("normalized forms differ:\n  a = %q\n  b = %q", gotA, gotB)
			}
		})
	}
}

// Masking must not go so far that genuinely different failures collapse
// together — that would merge unrelated incidents.
func TestNormalizeKeepsDistinctFailuresDistinct(t *testing.T) {
	t.Parallel()

	normalizer := mustNormalizer(t)
	timeout := normalizer.Normalize("dial tcp 10.96.0.5:5432: i/o timeout")
	refused := normalizer.Normalize("dial tcp 10.96.0.5:5432: connection refused")
	tlsError := normalizer.Normalize("x509: certificate has expired")

	if timeout == refused {
		t.Fatalf("timeout and connection-refused collapsed to the same text: %q", timeout)
	}
	if timeout == tlsError || refused == tlsError {
		t.Fatal("TLS failure collapsed into a dial failure")
	}
}

func TestNormalizeAppliesRedactionBeforeMasking(t *testing.T) {
	t.Parallel()

	normalizer := mustNormalizer(t, `Bearer\s+[A-Za-z0-9._-]+`, `password=\S+`)
	got := normalizer.Normalize("auth failed: Bearer eyJhbGciOiJIUzI1NiJ9.abc password=hunter2")

	for _, secret := range []string{"eyJhbGciOiJIUzI1NiJ9", "hunter2"} {
		if strings.Contains(got, strings.ToLower(secret)) {
			t.Fatalf("normalized text leaked %q: %q", secret, got)
		}
	}
	if !strings.Contains(got, "<redacted>") {
		t.Fatalf("normalized text = %q, want a <redacted> marker", got)
	}
}

func TestNewNormalizerRejectsInvalidRedactPattern(t *testing.T) {
	t.Parallel()

	// A silently dropped redaction rule would leak exactly what it protects,
	// so an invalid pattern must be a hard error.
	if _, err := NewNormalizer([]string{"([unclosed"}); err == nil {
		t.Fatal("NewNormalizer() error = nil, want a compile error")
	}
}

// Status codes carry real signal and are low-cardinality, so they survive
// masking even though bare numbers elsewhere do not.
func TestFailureTextKeepsStatusCodeButMasksMessageNumbers(t *testing.T) {
	t.Parallel()

	normalizer := mustNormalizer(t)
	got := normalizer.FailureText(Failure{
		ProbeType:      "http",
		StatusCode:     503,
		ExpectedStatus: 200,
		Message:        "upstream returned 503 after 4 retries",
	})

	if !strings.Contains(got, "status=503") {
		t.Fatalf("FailureText() = %q, want it to preserve status=503", got)
	}
	if !strings.Contains(got, "expected=200") {
		t.Fatalf("FailureText() = %q, want it to preserve expected=200", got)
	}
	if strings.Contains(got, "4 retries") {
		t.Fatalf("FailureText() = %q, want retry count masked", got)
	}
}

// Two probes hitting the same broken backend must produce identical failure
// text — that identity is the evidence correlation uses to merge them.
func TestFailureTextIsIdenticalForTheSameUpstreamFailure(t *testing.T) {
	t.Parallel()

	normalizer := mustNormalizer(t)
	payments := normalizer.FailureText(Failure{
		ProbeType: "http", StatusCode: 0, ExpectedStatus: 200,
		Message: "dial tcp 10.96.0.5:5432: i/o timeout after 2.1s",
	})
	orders := normalizer.FailureText(Failure{
		ProbeType: "http", StatusCode: 0, ExpectedStatus: 200,
		Message: "dial tcp 10.96.0.5:5432: i/o timeout after 1.7s",
	})

	if payments != orders {
		t.Fatalf("failure texts differ:\n  payments = %q\n  orders   = %q", payments, orders)
	}
}

// A different status code must NOT collapse — "everything is 503" and
// "everything is 404" are different incidents.
func TestFailureTextSeparatesDifferentStatusCodes(t *testing.T) {
	t.Parallel()

	normalizer := mustNormalizer(t)
	unavailable := normalizer.FailureText(Failure{ProbeType: "http", StatusCode: 503, ExpectedStatus: 200})
	notFound := normalizer.FailureText(Failure{ProbeType: "http", StatusCode: 404, ExpectedStatus: 200})

	if unavailable == notFound {
		t.Fatal("503 and 404 produced the same failure text")
	}
}

func TestBodyTextTruncatesBeforeNormalizing(t *testing.T) {
	t.Parallel()

	normalizer := mustNormalizer(t)
	body := strings.Repeat("a", 100) + "SENTINEL"

	got := normalizer.BodyText(body, 50)
	if strings.Contains(got, "sentinel") {
		t.Fatalf("BodyText() = %q, want content past maxBytes dropped", got)
	}
	if len(got) > 50 {
		t.Fatalf("BodyText() length = %d, want <= 50", len(got))
	}
}

// A health endpoint returns the same payload every check apart from a
// timestamp. Normalized, those are byte-identical — which is what lets the
// embedding cache skip almost all of the hot-path work.
func TestBodyTextIsStableForRepeatedHealthResponses(t *testing.T) {
	t.Parallel()

	normalizer := mustNormalizer(t)
	first := normalizer.BodyText(`{"status":"ok","checkedAt":"2026-08-30T22:14:05Z","uptime":417}`, 4096)
	second := normalizer.BodyText(`{"status":"ok","checkedAt":"2026-08-31T09:02:44Z","uptime":455}`, 4096)

	if first != second {
		t.Fatalf("health bodies did not normalize identically:\n  %q\n  %q", first, second)
	}
}

// The signal drift exists to catch: same shape, different meaning.
func TestBodyTextSeparatesEmptyResultSetFromPopulatedOne(t *testing.T) {
	t.Parallel()

	normalizer := mustNormalizer(t)
	populated := normalizer.BodyText(`{"items":[{"id":1,"name":"widget"},{"id":2,"name":"gadget"}]}`, 4096)
	empty := normalizer.BodyText(`{"items":[]}`, 4096)

	if populated == empty {
		t.Fatal("an empty result set normalized identically to a populated one")
	}
}
