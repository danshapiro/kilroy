package dot

import "testing"

func TestParse_RejectsMissingCommasBetweenAttrs(t *testing.T) {
	_, err := Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [label="A" prompt="missing comma"]
  start -> a -> exit
}
`))
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestParse_RejectsUndirectedEdges(t *testing.T) {
	_, err := Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  start -- exit
}
`))
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}
