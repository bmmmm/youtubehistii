// SPDX-License-Identifier: GPL-3.0-or-later

package taxonomy

import "math"

// Cosine returns the cosine similarity of two vectors; a zero or
// length-mismatched vector yields 0, never NaN.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// Centroid returns the weighted mean of the vectors, L2-normalized. Weights
// below 1 count as 1 so an unwatched label still pulls its own tiny weight.
func Centroid(vecs [][]float32, weights []int) []float32 {
	if len(vecs) == 0 {
		return nil
	}
	sum := make([]float64, len(vecs[0]))
	for i, v := range vecs {
		w := float64(max(weights[i], 1))
		for j := range v {
			sum[j] += w * float64(v[j])
		}
	}
	var norm float64
	for _, x := range sum {
		norm += x * x
	}
	norm = math.Sqrt(norm)
	out := make([]float32, len(sum))
	if norm == 0 {
		return out
	}
	for j, x := range sum {
		out[j] = float32(x / norm)
	}
	return out
}
