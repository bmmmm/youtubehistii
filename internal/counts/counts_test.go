package counts

import (
	"reflect"
	"testing"
)

func TestKeysOrdersByCountThenName(t *testing.T) {
	m := map[string]int{"b": 2, "c": 5, "a": 2, "d": 1}
	want := []string{"c", "a", "b", "d"}
	for i := 0; i < 10; i++ { // map iteration is random; the order must not be
		if got := Keys(m); !reflect.DeepEqual(got, want) {
			t.Fatalf("Keys() = %v, want %v", got, want)
		}
	}
}

func TestKeysEmpty(t *testing.T) {
	if got := Keys(map[string]int{}); len(got) != 0 {
		t.Fatalf("Keys(empty) = %v, want empty", got)
	}
}
