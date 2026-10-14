package catalog

import (
	"reflect"
	"testing"
)

func TestMoodsOf(t *testing.T) {
	set := func(xs ...string) map[string]bool {
		m := map[string]bool{}
		for _, x := range xs {
			m[x] = true
		}
		return m
	}
	cases := []struct {
		g, th, m []string
		ttb      int64
		want     []string
	}{
		{[]string{"simulator", "indie"}, []string{"sandbox"}, []string{"single player", "co-operative"}, 3000, []string{"cozy", "co-op"}},
		{[]string{"adventure"}, []string{"horror", "mystery"}, nil, 300, []string{"short", "story-rich", "spooky"}},
		{[]string{"platform", "indie"}, nil, nil, 0, []string{"challenging"}},
		{[]string{"puzzle"}, []string{"horror"}, nil, 0, []string{"spooky"}},
	}
	for i, c := range cases {
		if got := moodsOf(set(c.g...), set(c.th...), set(c.m...), c.ttb); !reflect.DeepEqual(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
			t.Errorf("case %d: %v, want %v", i, got, c.want)
		}
	}
}
