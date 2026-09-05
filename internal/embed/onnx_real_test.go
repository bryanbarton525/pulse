//go:build onnx

package embed

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func loadRealONNX(t *testing.T) Embedder {
	t.Helper()

	runtimePath := os.Getenv(ONNXSharedLibraryPathEnv)
	if runtimePath == "" {
		t.Skipf("%s is not set; point it at libonnxruntime.so to run the real ONNX test", ONNXSharedLibraryPathEnv)
	}
	if _, err := os.Stat(runtimePath); err != nil {
		t.Skipf("ONNX Runtime library is unavailable: %v", err)
	}

	root := os.Getenv("PULSE_MODELS_DIR")
	if root == "" {
		root = filepath.Join("..", "..", "hack", "models")
	}
	modelPath := filepath.Join(root, "minilm", "model.onnx")
	vocabPath := filepath.Join(root, "minilm", "vocab.txt")
	if _, err := os.Stat(modelPath); err != nil {
		t.Skipf("real ONNX model is unavailable (run `make fetch-models`): %v", err)
	}

	model, err := LoadONNX(modelPath, vocabPath, 256)
	if err != nil {
		t.Fatalf("LoadONNX() error = %v", err)
	}
	t.Cleanup(func() { _ = model.Close() })
	return model
}

func TestRealONNXModelEmbedsSemantically(t *testing.T) {
	model := loadRealONNX(t)
	vectors, err := model.Embed(context.Background(), []string{
		"dial tcp: connection refused",
		"the network connection was rejected",
		"the user profile was updated successfully",
	})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if got := model.Dimensions(); got != 384 {
		t.Fatalf("Dimensions() = %d, want 384 for all-MiniLM-L6-v2", got)
	}
	if got := model.Space(); got != SpaceMiniLM {
		t.Fatalf("Space() = %q, want %q", got, SpaceMiniLM)
	}
	if near, far := Distance(vectors[0], vectors[1]), Distance(vectors[0], vectors[2]); near >= far {
		t.Fatalf("related failure text is not closer: near %.4f, far %.4f", near, far)
	}
}

func TestRealONNXModelDistinguishesDemoFailureShapes(t *testing.T) {
	model := loadRealONNX(t)
	texts := []string{
		"type=http status=529 expected=200 message=expected <num> but got <num>",
		"type=http status=529 expected=200 message=expected <num> but got <num>",
		"type=http status=200 expected=204 message=expected <num> but got <num>",
		`type=http status=200 expected=200 message=journey step "read-session": response body did not contain "authenticated"`,
		"type=http status=200 expected=200 message=mcp required tool health.check was not advertised",
		"type=grpc message=grpc health check returned not_serving",
	}
	vectors, err := model.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	exactSimilarity := 1 - Distance(vectors[0], vectors[1])
	distinctSimilarity := 1 - Distance(vectors[0], vectors[2])
	t.Logf("exact replay similarity %.9f; distinct HTTP contract similarity %.9f",
		exactSimilarity, distinctSimilarity)
	if exactSimilarity <= distinctSimilarity {
		t.Fatalf("exact replay similarity %.9f <= distinct shape %.9f",
			exactSimilarity, distinctSimilarity)
	}
	for index := 2; index < len(vectors); index++ {
		t.Logf("outage versus %q similarity %.9f", texts[index], 1-Distance(vectors[0], vectors[index]))
	}
}
