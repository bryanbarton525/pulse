package embed

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realModelPaths locates the converted models produced by `make fetch-models`.
// These are gitignored, so every test here skips when they are absent and CI
// stays green without a 210 MiB download.
func realModelPaths(t *testing.T) (modelPath, vocabPath string) {
	t.Helper()

	root := os.Getenv("PULSE_MODELS_DIR")
	if root == "" {
		root = filepath.Join("..", "..", "hack", "models")
	}

	modelPath = filepath.Join(root, "potion", "model.bin")
	vocabPath = filepath.Join(root, "potion", "vocab.txt")

	if _, err := os.Stat(modelPath); err != nil {
		t.Skipf("real model not present (run `make fetch-models`): %v", err)
	}

	return modelPath, vocabPath
}

func loadRealPotion(t *testing.T) *PotionEmbedder {
	t.Helper()

	modelPath, vocabPath := realModelPaths(t)
	embedder, err := LoadPotion(modelPath, vocabPath, 256)
	if err != nil {
		t.Fatalf("LoadPotion() error = %v", err)
	}
	return embedder
}

func TestRealPotionModelLoads(t *testing.T) {
	t.Parallel()

	embedder := loadRealPotion(t)
	if got := embedder.Dimensions(); got != 512 {
		t.Fatalf("Dimensions() = %d, want 512 for potion-base-32M", got)
	}
	if got := embedder.Space(); got != SpacePotion {
		t.Fatalf("Space() = %q, want %q", got, SpacePotion)
	}
}

// The property everything downstream depends on: text that means the same
// thing lands closer together than text that does not. If this fails, drift
// thresholds and correlation similarity are meaningless.
func TestRealPotionModelOrdersFailureTextSemantically(t *testing.T) {
	t.Parallel()

	embedder := loadRealPotion(t)
	ctx := context.Background()

	cases := []struct {
		name              string
		anchor, near, far string
	}{
		{
			name:   "connection failures",
			anchor: "dial tcp <ip>: i/o timeout",
			near:   "dial tcp <ip>: connection refused",
			far:    "certificate has expired or is not yet valid",
		},
		{
			name:   "database errors",
			anchor: "fatal: could not connect to the database server",
			near:   "error: database connection pool exhausted",
			far:    "user profile updated successfully",
		},
		{
			name:   "empty versus populated payloads",
			anchor: `{"items": []}`,
			near:   `{"items": [], "total": 0}`,
			far:    `{"items": [{"id": 1, "name": "widget"}, {"id": 2, "name": "gadget"}]}`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			vectors, err := embedder.Embed(ctx,
				[]string{testCase.anchor, testCase.near, testCase.far})
			if err != nil {
				t.Fatalf("Embed() error = %v", err)
			}

			nearDistance := Distance(vectors[0], vectors[1])
			farDistance := Distance(vectors[0], vectors[2])

			if nearDistance >= farDistance {
				t.Fatalf("related text is not closer:\n  near %.4f (%q)\n  far  %.4f (%q)",
					nearDistance, testCase.near, farDistance, testCase.far)
			}
			t.Logf("near %.4f, far %.4f", nearDistance, farDistance)
		})
	}
}

// A health endpoint whose body is unchanged after normalization must score
// essentially zero, or drift would fire constantly on healthy services.
func TestRealPotionModelScoresIdenticalBodiesAtZero(t *testing.T) {
	t.Parallel()

	embedder := loadRealPotion(t)
	body := `{"status": "ok", "version": "1.4.2", "checks": {"db": "ok", "cache": "ok"}}`

	vectors, err := embedder.Embed(context.Background(), []string{body, body})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}

	if distance := Distance(vectors[0], vectors[1]); distance > 1e-6 {
		t.Fatalf("identical bodies scored %v, want ~0", distance)
	}
}

// The real signal drift exists to catch, measured against a plausible default
// threshold rather than a synthetic one.
func TestRealPotionModelSeparatesSilentFailureFromNormal(t *testing.T) {
	t.Parallel()

	embedder := loadRealPotion(t)
	ctx := context.Background()

	normal := `{"items": [{"id": 1, "name": "widget"}, {"id": 2, "name": "gadget"}], "total": 2}`
	silentFailure := `<html><body><h1>Service Temporarily Unavailable</h1></body></html>`

	vectors, err := embedder.Embed(ctx, []string{normal, silentFailure})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}

	distance := Distance(vectors[0], vectors[1])
	if distance < 0.15 {
		t.Fatalf("a maintenance page scored %.4f against a normal payload, "+
			"below the default 0.15 threshold — drift would not fire", distance)
	}
	t.Logf("maintenance page scored %.4f against normal payload", distance)
}

// The scale claim: the hot path must absorb hundreds of embeddings per second
// per core, since body drift runs on every passing check.
func BenchmarkRealPotionEmbed(b *testing.B) {
	root := os.Getenv("PULSE_MODELS_DIR")
	if root == "" {
		root = filepath.Join("..", "..", "hack", "models")
	}
	modelPath := filepath.Join(root, "potion", "model.bin")
	if _, err := os.Stat(modelPath); err != nil {
		b.Skipf("real model not present (run `make fetch-models`): %v", err)
	}

	embedder, err := LoadPotion(modelPath, filepath.Join(root, "potion", "vocab.txt"), 256)
	if err != nil {
		b.Fatalf("LoadPotion() error = %v", err)
	}

	// A realistic API response body, around 2 KiB.
	var builder strings.Builder
	builder.WriteString(`{"items":[`)
	for index := range 40 {
		if index > 0 {
			builder.WriteString(",")
		}
		fmt.Fprintf(&builder,
			`{"id":%d,"name":"widget-%d","status":"active","updated":"<ts>"}`, index, index)
	}
	builder.WriteString(`],"total":40}`)
	body := builder.String()

	ctx := context.Background()
	texts := []string{body}

	b.ReportAllocs()
	b.SetBytes(int64(len(body)))

	for b.Loop() {
		if _, err := embedder.Embed(ctx, texts); err != nil {
			b.Fatalf("Embed() error = %v", err)
		}
	}
}
