package embed

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// Pulse's static-embedding file format.
//
// Model2Vec publishes weights as safetensors, which would need a parser and a
// dtype matrix in Go for no benefit — at load time this is just one dense
// float32 matrix. `make fetch-models` converts it once at build time into the
// layout below, so the runtime loader is a header read and a single slice.
//
//	 0..7   magic "PULSEM2V"
//	 8..11  version   (uint32, little endian)
//	12..15  dimensions(uint32)
//	16..19  row count (uint32)
//	20..    rows*dimensions float32, row-major, little endian
const (
	potionMagic      = "PULSEM2V"
	potionVersion    = 1
	potionHeaderSize = 20
)

// PotionEmbedder implements static Model2Vec embeddings.
//
// Inference is a token lookup and a mean. Model2Vec bakes its PCA and SIF
// weighting into the matrix at distillation time, so there is genuinely nothing
// else to do at runtime — which is the entire reason this tier can absorb the
// body-drift firehose on a fraction of a core.
type PotionEmbedder struct {
	tokenizer  *WordPiece
	matrix     []float32
	dimensions int
	rows       int
	maxTokens  int
}

// LoadPotion reads a converted static model and its vocabulary.
func LoadPotion(modelPath, vocabPath string, maxTokens int) (*PotionEmbedder, error) {
	raw, err := os.ReadFile(modelPath)
	if err != nil {
		return nil, fmt.Errorf("reading static model %s: %w", modelPath, err)
	}
	if len(raw) < potionHeaderSize {
		return nil, fmt.Errorf("static model %s is truncated", modelPath)
	}
	if string(raw[:8]) != potionMagic {
		return nil, fmt.Errorf("static model %s has an unexpected format; run `make fetch-models`", modelPath)
	}

	version := binary.LittleEndian.Uint32(raw[8:12])
	if version != potionVersion {
		return nil, fmt.Errorf("static model %s has version %d, want %d", modelPath, version, potionVersion)
	}

	dimensions := int(binary.LittleEndian.Uint32(raw[12:16]))
	rows := int(binary.LittleEndian.Uint32(raw[16:20]))
	if dimensions <= 0 || rows <= 0 {
		return nil, fmt.Errorf("static model %s declares %d rows of %d dimensions", modelPath, rows, dimensions)
	}

	expected := potionHeaderSize + rows*dimensions*4
	if len(raw) < expected {
		return nil, fmt.Errorf(
			"static model %s is %d bytes, want %d for %d rows of %d dimensions",
			modelPath, len(raw), expected, rows, dimensions)
	}

	matrix := make([]float32, rows*dimensions)
	for index := range matrix {
		offset := potionHeaderSize + index*4
		matrix[index] = math.Float32frombits(binary.LittleEndian.Uint32(raw[offset : offset+4]))
	}

	tokenizer, err := LoadWordPiece(vocabPath, true)
	if err != nil {
		return nil, err
	}
	if tokenizer.Size() > rows {
		return nil, fmt.Errorf(
			"vocab %s has %d tokens but model %s has only %d rows",
			vocabPath, tokenizer.Size(), modelPath, rows)
	}

	if maxTokens <= 0 {
		maxTokens = 256
	}

	return &PotionEmbedder{
		tokenizer:  tokenizer,
		matrix:     matrix,
		dimensions: dimensions,
		rows:       rows,
		maxTokens:  maxTokens,
	}, nil
}

// Space implements Embedder.
func (p *PotionEmbedder) Space() string { return SpacePotion }

// Dimensions implements Embedder.
func (p *PotionEmbedder) Dimensions() int { return p.dimensions }

// Close implements Embedder. Nothing to release — the matrix is plain memory.
func (p *PotionEmbedder) Close() error { return nil }

// Embed implements Embedder.
//
// There is no shared mutable state here, so unlike the ONNX path this needs no
// lock and every probe goroutine can embed concurrently.
func (p *PotionEmbedder) Embed(ctx context.Context, texts []string) ([]Vector, error) {
	vectors := make([]Vector, 0, len(texts))

	for _, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vectors = append(vectors, p.embedOne(text))
	}

	return vectors, nil
}

func (p *PotionEmbedder) embedOne(text string) Vector {
	// No special tokens: with no attention layers, [CLS] and [SEP] would be two
	// constant vectors pulling every mean toward the same point.
	ids := p.tokenizer.Encode(text, EncodeOptions{MaxTokens: p.maxTokens})

	values := make([]float32, p.dimensions)
	counted := 0

	for _, id := range ids {
		if id < 0 || int(id) >= p.rows {
			continue
		}
		row := p.matrix[int(id)*p.dimensions : (int(id)+1)*p.dimensions]
		for index, value := range row {
			values[index] += value
		}
		counted++
	}

	// An empty or fully out-of-vocabulary document yields the zero vector.
	// Cosine against zero is defined as 0 similarity, so this reads as
	// "maximally unlike the baseline" rather than crashing.
	if counted > 0 {
		inverse := float32(1) / float32(counted)
		for index := range values {
			values[index] *= inverse
		}
		normalizeInPlace(values)
	}

	return Vector{Space: SpacePotion, Values: values}
}
