package embed

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// writePotionModel builds a model file in Pulse's converted static format.
// rows[i] is the vector for the token with ID i.
func writePotionModel(t *testing.T, rows [][]float32) string {
	t.Helper()

	if len(rows) == 0 {
		t.Fatal("writePotionModel needs at least one row")
	}
	dimensions := len(rows[0])

	buffer := make([]byte, 0, potionHeaderSize+len(rows)*dimensions*4)
	buffer = append(buffer, []byte(potionMagic)...)
	buffer = binary.LittleEndian.AppendUint32(buffer, potionVersion)
	buffer = binary.LittleEndian.AppendUint32(buffer, uint32(dimensions))
	buffer = binary.LittleEndian.AppendUint32(buffer, uint32(len(rows)))

	for _, row := range rows {
		if len(row) != dimensions {
			t.Fatalf("row width %d, want %d", len(row), dimensions)
		}
		for _, value := range row {
			buffer = binary.LittleEndian.AppendUint32(buffer, math.Float32bits(value))
		}
	}

	path := filepath.Join(t.TempDir(), "model.bin")
	if err := os.WriteFile(path, buffer, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

// testPotion builds an embedder over the shared test vocabulary, with vectors
// chosen so that connection/refused sit near each other and cafe sits apart.
func testPotion(t *testing.T) *PotionEmbedder {
	t.Helper()

	rows := [][]float32{
		{0, 0, 0, 0},      // 0 [PAD]
		{0, 0, 0, 1},      // 1 [UNK]
		{0, 0, 1, 0},      // 2 [CLS]
		{0, 0, 1, 0},      // 3 [SEP]
		{1, 0.1, 0, 0},    // 4 connection
		{1, -0.1, 0, 0},   // 5 refused
		{0.9, 0.2, 0, 0},  // 6 time
		{0.9, -0.2, 0, 0}, // 7 ##out
		{0.8, 0.3, 0, 0},  // 8 un
		{0.8, -0.3, 0, 0}, // 9 ##available
		{0, 1, 0, 0},      // 10 :
		{0, 0, 0, -1},     // 11 cafe
		{0.95, 0, 0, 0},   // 12 upstream
	}

	modelPath := writePotionModel(t, rows)
	vocabPath := writeVocab(t, testVocabTokens())

	embedder, err := LoadPotion(modelPath, vocabPath, 128)
	if err != nil {
		t.Fatalf("LoadPotion() error = %v", err)
	}
	return embedder
}

func embedOne(t *testing.T, embedder Embedder, text string) Vector {
	t.Helper()

	vectors, err := embedder.Embed(context.Background(), []string{text})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(vectors) != 1 {
		t.Fatalf("Embed() returned %d vectors, want 1", len(vectors))
	}
	return vectors[0]
}

func TestPotionEmbedderMetadata(t *testing.T) {
	t.Parallel()

	embedder := testPotion(t)
	if got := embedder.Space(); got != SpacePotion {
		t.Fatalf("Space() = %q, want %q", got, SpacePotion)
	}
	if got := embedder.Dimensions(); got != 4 {
		t.Fatalf("Dimensions() = %d, want 4", got)
	}
	if err := embedder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestPotionEmbedIsNormalized(t *testing.T) {
	t.Parallel()

	vector := embedOne(t, testPotion(t), "connection refused")

	var sum float64
	for _, value := range vector.Values {
		sum += float64(value) * float64(value)
	}
	if math.Abs(math.Sqrt(sum)-1) > 1e-5 {
		t.Fatalf("vector magnitude = %v, want 1", math.Sqrt(sum))
	}
}

// The property the whole feature rests on: related text lands closer together
// than unrelated text.
func TestPotionEmbedPlacesRelatedTextCloser(t *testing.T) {
	t.Parallel()

	embedder := testPotion(t)
	base := embedOne(t, embedder, "connection refused")
	related := embedOne(t, embedder, "upstream unavailable")
	unrelated := embedOne(t, embedder, "cafe")

	near := Distance(base, related)
	far := Distance(base, unrelated)

	if near >= far {
		t.Fatalf("related distance %v should be below unrelated distance %v", near, far)
	}
}

func TestPotionEmbedIsDeterministic(t *testing.T) {
	t.Parallel()

	embedder := testPotion(t)
	first := embedOne(t, embedder, "connection refused")
	second := embedOne(t, embedder, "connection refused")

	if Distance(first, second) > 1e-6 {
		t.Fatal("the same text produced different vectors")
	}
}

// An empty document must yield a zero vector rather than NaN, so a probe that
// returns an empty body cannot poison a baseline.
func TestPotionEmbedEmptyTextYieldsZeroVector(t *testing.T) {
	t.Parallel()

	vector := embedOne(t, testPotion(t), "")
	for _, value := range vector.Values {
		if value != 0 || math.IsNaN(float64(value)) {
			t.Fatalf("empty text produced %v, want an all-zero vector", vector.Values)
		}
	}
}

func TestPotionEmbedBatchPreservesOrder(t *testing.T) {
	t.Parallel()

	embedder := testPotion(t)
	texts := []string{"connection refused", "cafe", "upstream"}

	batch, err := embedder.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(batch) != len(texts) {
		t.Fatalf("Embed() returned %d vectors, want %d", len(batch), len(texts))
	}

	for index, text := range texts {
		single := embedOne(t, embedder, text)
		if Distance(batch[index], single) > 1e-6 {
			t.Fatalf("batch position %d does not match the single-text embedding of %q", index, text)
		}
	}
}

func TestLoadPotionRejectsCorruptFiles(t *testing.T) {
	t.Parallel()

	vocabPath := writeVocab(t, testVocabTokens())

	t.Run("bad magic", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "model.bin")
		if err := os.WriteFile(path, make([]byte, 64), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if _, err := LoadPotion(path, vocabPath, 128); err == nil {
			t.Fatal("LoadPotion() error = nil, want a format error")
		}
	})

	t.Run("truncated body", func(t *testing.T) {
		t.Parallel()

		buffer := []byte(potionMagic)
		buffer = binary.LittleEndian.AppendUint32(buffer, potionVersion)
		buffer = binary.LittleEndian.AppendUint32(buffer, 4)
		buffer = binary.LittleEndian.AppendUint32(buffer, 100) // claims 100 rows
		// ...but supplies none.

		path := filepath.Join(t.TempDir(), "model.bin")
		if err := os.WriteFile(path, buffer, 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if _, err := LoadPotion(path, vocabPath, 128); err == nil {
			t.Fatal("LoadPotion() error = nil, want a truncation error")
		}
	})

	t.Run("vocab larger than matrix", func(t *testing.T) {
		t.Parallel()

		// Two rows cannot serve a thirteen-token vocabulary.
		path := writePotionModel(t, [][]float32{{1, 0}, {0, 1}})
		if _, err := LoadPotion(path, vocabPath, 128); err == nil {
			t.Fatal("LoadPotion() error = nil, want a vocab/matrix mismatch error")
		}
	})
}

func TestPotionEmbedRespectsContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := testPotion(t).Embed(ctx, []string{"connection refused"}); err == nil {
		t.Fatal("Embed() error = nil, want a context error")
	}
}
