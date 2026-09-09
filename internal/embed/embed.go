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

// Package embed turns text into vectors, using one of two very different
// models depending on how hot the code path is.
//
// Pulse embeds on two schedules that differ by three orders of magnitude:
//
//   - Body drift runs on EVERY passing check. At a few thousand canaries that
//     is hundreds of embeddings per second, sustained forever. This path uses
//     static Model2Vec ("potion") embeddings, where inference is a token
//     lookup plus a mean — no transformer forward pass, no cgo, microseconds
//     per document.
//   - Failure correlation and novelty run only on failures, which at any
//     reasonable availability is a fraction of one per second. That path can
//     afford a real transformer (MiniLM via ONNX Runtime), which is where
//     semantic precision actually changes decisions.
package embed

import (
	"context"
	"fmt"
	"math"
)

// Embedding spaces. A vector is only comparable to another vector from the
// same space.
const (
	SpacePotion = "potion"
	SpaceMiniLM = "minilm"
)

// Vector is an embedding tagged with the space that produced it.
//
// The tag exists because Pulse holds vectors from two different models in
// memory at once, and a cosine distance between them is meaningless — it would
// silently produce a plausible-looking number and corrupt every drift score or
// correlation decision that consumed it. Tagging makes that mistake loud.
type Vector struct {
	Space  string
	Values []float32
}

// Embedder converts text into vectors.
type Embedder interface {
	// Embed returns one vector per input text, in order.
	Embed(ctx context.Context, texts []string) ([]Vector, error)

	// Space identifies the embedding space, for comparability checks.
	Space() string

	// Dimensions is the width of the vectors this embedder produces.
	Dimensions() int

	// Close releases any model resources.
	Close() error
}

// ErrSpaceMismatch describes an attempt to compare incomparable vectors.
type ErrSpaceMismatch struct {
	Left, Right string
}

func (e ErrSpaceMismatch) Error() string {
	return fmt.Sprintf(
		"embed: cannot compare vectors from different spaces (%q and %q); "+
			"drift baselines live in the hot-path space and correlation in the cold-path space",
		e.Left, e.Right)
}

// Cosine returns the cosine similarity of two vectors, in [-1, 1].
//
// It PANICS when the spaces differ. That is deliberate: a space mismatch cannot
// be caused by user input or by a failing network call, only by a bug in
// Pulse's own wiring. Returning an error would invite a caller to log and
// continue with a meaningless score, which is exactly the failure mode the
// space tag exists to prevent.
func Cosine(left, right Vector) float64 {
	if left.Space != right.Space {
		panic(ErrSpaceMismatch{Left: left.Space, Right: right.Space})
	}
	if len(left.Values) != len(right.Values) {
		panic(fmt.Sprintf("embed: vector length mismatch (%d and %d) within space %q",
			len(left.Values), len(right.Values), left.Space))
	}

	var dot, leftNorm, rightNorm float64
	for index := range left.Values {
		l := float64(left.Values[index])
		r := float64(right.Values[index])
		dot += l * r
		leftNorm += l * l
		rightNorm += r * r
	}

	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}

	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}

// Distance is the cosine distance, in [0, 2]. Zero means identical meaning.
func Distance(left, right Vector) float64 {
	return 1 - Cosine(left, right)
}

// normalizeInPlace scales a vector to unit length. Embedders normalize their
// output so that later cosine work is a plain dot product and so that averaging
// vectors into a baseline is well behaved.
func normalizeInPlace(values []float32) {
	var sum float64
	for _, value := range values {
		sum += float64(value) * float64(value)
	}
	if sum == 0 {
		return
	}

	scale := float32(1 / math.Sqrt(sum))
	for index := range values {
		values[index] *= scale
	}
}
