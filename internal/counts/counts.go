// Package counts holds the one shared rule for ordering count maps.
package counts

import "sort"

// Keys returns every key of a count map, count desc with the name as
// tie-break. The order must be deterministic: anything that feeds a prompt
// or a vector must not reshuffle between runs — a prompt that reshuffles
// invites the model to reshuffle its answers with it. The same rule renders
// human-facing count lists, so a report does not reorder between runs either.
func Keys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}
