package main

import (
	"testing"
)

func TestZZAVDelta(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"/tmp/fftest/cap5.ts", "0"},
		{"/tmp/fftest/capo.ts", "10"},
	} {
		d, err := tsAVDelta(tc.file)
		if err != nil {
			t.Errorf("%s: %v", tc.file, err)
			continue
		}
		t.Logf("%s: delta=%.4f s", tc.file, d)
		_ = tc.want
	}
}