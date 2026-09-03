//go:build onnx

package embed

import (
	"context"
	"fmt"
	"os"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// onnxRuntimeOnce guards process-wide ONNX Runtime initialization. The library
// keeps a single global environment, so initializing twice is an error.
var (
	onnxRuntimeOnce sync.Once
	onnxRuntimeErr  error
)

// ONNXSharedLibraryPathEnv overrides where the runtime .so is loaded from.
const ONNXSharedLibraryPathEnv = "ONNXRUNTIME_SHARED_LIBRARY_PATH"

func initONNXRuntime() error {
	onnxRuntimeOnce.Do(func() {
		// Always set the path explicitly. The library's default is to dlopen
		// "onnxruntime.so" from the loader path, which is fragile in a
		// container built by hand.
		if path := os.Getenv(ONNXSharedLibraryPathEnv); path != "" {
			ort.SetSharedLibraryPath(path)
		}
		onnxRuntimeErr = ort.InitializeEnvironment()
	})
	return onnxRuntimeErr
}

// ONNXEmbedder runs a BERT-family sentence transformer in process.
type ONNXEmbedder struct {
	tokenizer *WordPiece
	maxTokens int

	// mu serializes Run(). ONNX Runtime sessions are not goroutine-safe, and
	// on the cold path there is one embed call per failure, so contention here
	// is irrelevant.
	mu      sync.Mutex
	session *ort.DynamicAdvancedSession

	dimensions int
}

// LoadONNX opens an ONNX sentence transformer and its vocabulary.
func LoadONNX(modelPath, vocabPath string, maxTokens int) (Embedder, error) {
	if err := initONNXRuntime(); err != nil {
		return nil, fmt.Errorf("initializing ONNX Runtime: %w", err)
	}

	tokenizer, err := LoadWordPiece(vocabPath, true)
	if err != nil {
		return nil, err
	}
	if maxTokens <= 0 {
		maxTokens = 256
	}

	session, err := ort.NewDynamicAdvancedSession(
		modelPath,
		[]string{"input_ids", "attention_mask", "token_type_ids"},
		[]string{"last_hidden_state"},
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("opening ONNX model %s: %w", modelPath, err)
	}

	return &ONNXEmbedder{tokenizer: tokenizer, maxTokens: maxTokens, session: session}, nil
}

// ONNXCompiledIn reports whether this binary can run the in-process transformer.
func ONNXCompiledIn() bool { return true }

// Space implements Embedder.
func (o *ONNXEmbedder) Space() string { return SpaceMiniLM }

// Dimensions implements Embedder.
func (o *ONNXEmbedder) Dimensions() int { return o.dimensions }

// Close implements Embedder.
func (o *ONNXEmbedder) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.session != nil {
		o.session.Destroy()
		o.session = nil
	}
	return nil
}

// Embed implements Embedder: tokenize, run the transformer, mean-pool the last
// hidden state over the attention mask, and L2-normalize.
func (o *ONNXEmbedder) Embed(ctx context.Context, texts []string) ([]Vector, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	batch := len(texts)
	encoded := make([][]int32, batch)
	longest := 1
	for index, text := range texts {
		encoded[index] = o.tokenizer.Encode(text, EncodeOptions{
			AddSpecialTokens: true,
			MaxTokens:        o.maxTokens,
		})
		if len(encoded[index]) > longest {
			longest = len(encoded[index])
		}
	}

	inputIDs := make([]int64, batch*longest)
	attention := make([]int64, batch*longest)
	tokenTypes := make([]int64, batch*longest)
	for row, ids := range encoded {
		for column, id := range ids {
			inputIDs[row*longest+column] = int64(id)
			attention[row*longest+column] = 1
		}
	}

	shape := ort.NewShape(int64(batch), int64(longest))
	idTensor, err := ort.NewTensor(shape, inputIDs)
	if err != nil {
		return nil, fmt.Errorf("building input_ids tensor: %w", err)
	}
	defer idTensor.Destroy()

	maskTensor, err := ort.NewTensor(shape, attention)
	if err != nil {
		return nil, fmt.Errorf("building attention_mask tensor: %w", err)
	}
	defer maskTensor.Destroy()

	typeTensor, err := ort.NewTensor(shape, tokenTypes)
	if err != nil {
		return nil, fmt.Errorf("building token_type_ids tensor: %w", err)
	}
	defer typeTensor.Destroy()

	outputs := []ort.Value{nil}

	o.mu.Lock()
	err = o.session.Run([]ort.Value{idTensor, maskTensor, typeTensor}, outputs)
	o.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("running ONNX session: %w", err)
	}

	hidden, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("ONNX model returned an unexpected output type %T", outputs[0])
	}
	defer hidden.Destroy()

	outputShape := hidden.GetShape()
	if len(outputShape) != 3 {
		return nil, fmt.Errorf("ONNX model returned rank %d output, want 3", len(outputShape))
	}
	dimensions := int(outputShape[2])
	o.dimensions = dimensions

	data := hidden.GetData()
	vectors := make([]Vector, batch)
	for row := range batch {
		values := make([]float32, dimensions)
		counted := 0

		for column := range longest {
			if attention[row*longest+column] == 0 {
				continue
			}
			offset := (row*longest + column) * dimensions
			for index := range dimensions {
				values[index] += data[offset+index]
			}
			counted++
		}

		if counted > 0 {
			inverse := float32(1) / float32(counted)
			for index := range values {
				values[index] *= inverse
			}
			normalizeInPlace(values)
		}

		vectors[row] = Vector{Space: SpaceMiniLM, Values: values}
	}

	return vectors, nil
}
