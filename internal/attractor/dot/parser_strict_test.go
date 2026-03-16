package dot

import "testing"

func TestParse_RejectsTrailingTokensAfterGraph(t *testing.T) {
	_, err := Parse([]byte(`
digraph A { start [shape=Mdiamond] exit [shape=Msquare] start -> exit }
digraph B { start [shape=Mdiamond] exit [shape=Msquare] start -> exit }
`))
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestParse_AllowsOptionalTrailingSemicolon(t *testing.T) {
	_, err := Parse([]byte(`digraph A { start [shape=Mdiamond] exit [shape=Msquare] start -> exit };`))
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
}
