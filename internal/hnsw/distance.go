package hnsw

import (
	"math"
	"reflect"
)

// DistanceFunc is a function that computes the distance between two vectors.
type DistanceFunc func(a, b []float32) float32

// UnitCosineDistance avoids recomputing norms for unit-length index vectors.
func UnitCosineDistance(a, b []float32) float32 {
	var dot float32
	for i, x := range a {
		dot += x * b[i]
	}
	return 1 - dot
}

// CosineDistance computes the cosine distance between two vectors.
func CosineDistance(a, b []float32) float32 {
	var dot, aa, bb float32
	for i, x := range a {
		dot += x * b[i]
		aa += x * x
		bb += b[i] * b[i]
	}
	return 1 - dot/float32(math.Sqrt(float64(aa)*float64(bb)))
}

// EuclideanDistance computes the Euclidean distance between two vectors.
func EuclideanDistance(a, b []float32) float32 {
	// TODO: can we speedup with vek?
	var sum float32 = 0
	for i := range a {
		diff := a[i] - b[i]
		sum += diff * diff
	}
	return float32(math.Sqrt(float64(sum)))
}

var distanceFuncs = map[string]DistanceFunc{
	"unit-cosine": UnitCosineDistance,
	"euclidean":   EuclideanDistance,
	"cosine":      CosineDistance,
}

func distanceFuncToName(fn DistanceFunc) (string, bool) {
	for name, f := range distanceFuncs {
		fnptr := reflect.ValueOf(fn).Pointer()
		fptr := reflect.ValueOf(f).Pointer()
		if fptr == fnptr {
			return name, true
		}
	}
	return "", false
}

// RegisterDistanceFunc registers a distance function with a name.
// A distance function must be registered here before a graph can be
// exported and imported.
func RegisterDistanceFunc(name string, fn DistanceFunc) {
	distanceFuncs[name] = fn
}
