//go:build !onnx

package embed

import "errors"

// ErrONNXUnavailable is returned when a policy asks for the in-process
// transformer but this binary was built without it.
//
// The probe runner is deliberately built WITHOUT the onnx tag: it scales to N
// replicas and stays cgo-free on a distroless/static base. Only the
// single-replica incident engine carries ONNX Runtime, so only it can serve the
// cold path.
var ErrONNXUnavailable = errors.New(
	"embed: the onnx backend is not compiled into this binary; " +
		"use the incident engine image, or set model.cold.backend to \"http\"")

// LoadONNX reports that the ONNX backend is unavailable in this build.
func LoadONNX(_, _ string, _ int) (Embedder, error) {
	return nil, ErrONNXUnavailable
}

// ONNXCompiledIn reports whether this binary can run the in-process transformer.
func ONNXCompiledIn() bool { return false }
