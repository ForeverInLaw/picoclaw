package facts

import "math"

// Cosine computes the cosine similarity of two equal-length float32 vectors,
// given their precomputed L2 norms. Returns 0 when either norm is zero or
// the lengths disagree.
func Cosine(a []float32, normA float64, b []float32, normB float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	denom := normA * normB
	if denom == 0 {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot / denom
}

// Norm returns the L2 norm of v as a float64.
func Norm(v []float32) float64 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return math.Sqrt(s)
}
