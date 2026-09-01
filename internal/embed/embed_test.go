package embed

import (
	"errors"
	"math"
	"testing"
)

func TestCosineIdenticalAndOpposite(t *testing.T) {
	t.Parallel()

	a := Vector{Space: SpacePotion, Values: []float32{1, 0, 0}}
	b := Vector{Space: SpacePotion, Values: []float32{1, 0, 0}}
	c := Vector{Space: SpacePotion, Values: []float32{-1, 0, 0}}
	d := Vector{Space: SpacePotion, Values: []float32{0, 1, 0}}

	if got := Cosine(a, b); math.Abs(got-1) > 1e-6 {
		t.Fatalf("Cosine(identical) = %v, want 1", got)
	}
	if got := Cosine(a, c); math.Abs(got+1) > 1e-6 {
		t.Fatalf("Cosine(opposite) = %v, want -1", got)
	}
	if got := Cosine(a, d); math.Abs(got) > 1e-6 {
		t.Fatalf("Cosine(orthogonal) = %v, want 0", got)
	}
	if got := Distance(a, d); math.Abs(got-1) > 1e-6 {
		t.Fatalf("Distance(orthogonal) = %v, want 1", got)
	}
}

// A zero vector (an empty or fully out-of-vocabulary document) must not divide
// by zero or produce NaN — it reads as "no similarity".
func TestCosineHandlesZeroVector(t *testing.T) {
	t.Parallel()

	zero := Vector{Space: SpacePotion, Values: []float32{0, 0, 0}}
	other := Vector{Space: SpacePotion, Values: []float32{1, 2, 3}}

	got := Cosine(zero, other)
	if math.IsNaN(got) || got != 0 {
		t.Fatalf("Cosine(zero, other) = %v, want 0", got)
	}
}

// Mixing embedding spaces is a wiring bug that would silently corrupt every
// score downstream, so it must be impossible to ignore.
func TestCosinePanicsOnSpaceMismatch(t *testing.T) {
	t.Parallel()

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("Cosine() did not panic on a space mismatch")
		}
		var mismatch ErrSpaceMismatch
		if err, ok := recovered.(error); !ok || !errors.As(err, &mismatch) {
			t.Fatalf("recovered %#v, want an ErrSpaceMismatch", recovered)
		}
	}()

	Cosine(
		Vector{Space: SpacePotion, Values: []float32{1, 0}},
		Vector{Space: SpaceMiniLM, Values: []float32{1, 0}},
	)
}

func TestCosinePanicsOnLengthMismatch(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatal("Cosine() did not panic on a length mismatch")
		}
	}()

	Cosine(
		Vector{Space: SpacePotion, Values: []float32{1, 0}},
		Vector{Space: SpacePotion, Values: []float32{1, 0, 0}},
	)
}

func TestNormalizeInPlace(t *testing.T) {
	t.Parallel()

	values := []float32{3, 4}
	normalizeInPlace(values)

	if math.Abs(float64(values[0])-0.6) > 1e-6 || math.Abs(float64(values[1])-0.8) > 1e-6 {
		t.Fatalf("normalizeInPlace([3 4]) = %v, want [0.6 0.8]", values)
	}

	zero := []float32{0, 0}
	normalizeInPlace(zero)
	if zero[0] != 0 || zero[1] != 0 {
		t.Fatalf("normalizeInPlace(zero) = %v, want it left alone", zero)
	}
}
