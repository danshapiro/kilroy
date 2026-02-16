package runtime

import "testing"

func TestDecodeOutcomeJSON_PromotesTopLevelFailureClassAndSignature(t *testing.T) {
	raw := []byte(`{"status":"fail","failure_reason":"verbose prose","failure_class":"deterministic","failure_signature":"environmental_tooling_blocks"}`)
	out, err := DecodeOutcomeJSON(raw)
	if err != nil {
		t.Fatalf("DecodeOutcomeJSON: %v", err)
	}
	if got := out.Meta["failure_class"]; got != "deterministic" {
		t.Fatalf("meta.failure_class: got %v want deterministic", got)
	}
	if got := out.Meta["failure_signature"]; got != "environmental_tooling_blocks" {
		t.Fatalf("meta.failure_signature: got %v want environmental_tooling_blocks", got)
	}
}
