// SPDX-License-Identifier: GPL-3.0-or-later

package taxonomy

import (
	"reflect"
	"testing"
)

// TestParseNameBatchIsStrict pins the mapping guarantee. A name that lands on
// the wrong cluster is invisible afterwards — it just reads as a badly named
// subject — so anything less than a complete, unambiguous answer has to be
// refused rather than patched up.
func TestParseNameBatchIsStrict(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
		n     int
		want  []string // nil = expect an error
	}{
		{"clean", "1 jazz\n2 techno\n3 chess", 3, []string{"jazz", "techno", "chess"}},
		{"out of order", "3 chess\n1 jazz\n2 techno", 3, []string{"jazz", "techno", "chess"}},
		{"numbered with dots", "1. jazz\n2. techno", 2, []string{"jazz", "techno"}},
		{"prose around it", "Sure!\n```\n1 jazz\n2 techno\n```\nHope that helps", 2, []string{"jazz", "techno"}},
		{"missing one", "1 jazz\n2 techno", 3, nil},
		{"duplicate number", "1 jazz\n1 techno\n3 chess", 3, nil},
		{"number out of range", "1 jazz\n2 techno\n7 chess", 3, nil},
		{"unusable slug", "1 jazz\n2 ---", 2, nil},
		{"empty reply", "", 2, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseNameBatch(tc.reply, tc.n)
			if tc.want == nil {
				if err == nil {
					t.Fatalf("parsed %q into %v, want an error so the caller retries singly", tc.reply, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseNameBatch(%q) = %v", tc.reply, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
