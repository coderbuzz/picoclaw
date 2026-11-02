package anthropicprovider

import "testing"

func TestBuildParams_NativeAPIKeyDefaults(t *testing.T) {
	msgs := []Message{{
		Role:    "user",
		Content: "apa isi foto dan dokumen ini?",
		Media:   []string{"data:image/png;base64,aWNvbg==", "data:application/pdf;base64,JVBERi0=", "data:audio/ogg;base64,AAAA"},
	}}
	params, err := buildParams(msgs, nil, "claude-haiku-5-5",
		map[string]any{"temperature": 0.7, "thinking_level": "off"})
	if err != nil {
		t.Fatal(err)
	}

	if params.Temperature.Valid() {
		t.Error("temperature must be omitted for claude-haiku-5-5")
	}
	if params.CacheControl.Type != "ephemeral" {
		t.Errorf("cache_control = %q, want ephemeral", params.CacheControl.Type)
	}
	if params.Thinking.OfDisabled == nil {
		t.Error("thinking_level off must send thinking disabled")
	}

	blocks := params.Messages[0].Content
	if len(blocks) != 3 {
		t.Fatalf("content blocks = %d, want image + pdf + text (audio skipped)", len(blocks))
	}
	if blocks[0].OfImage == nil || blocks[1].OfDocument == nil || blocks[2].OfText == nil {
		t.Errorf("unexpected block order/types: %+v", blocks)
	}
}

func TestRejectsSampling(t *testing.T) {
	for model, want := range map[string]bool{
		"claude-haiku-5-5":  true,
		"claude-sonnet-5":   true,
		"claude-opus-4-8":   true,
		"claude-fable-5-1":  true,
		"claude-sonnet-4-6": false,
		"claude-sonnet-4-5": false,
		"claude-haiku-4-5":  false,
	} {
		if got := rejectsSampling(model); got != want {
			t.Errorf("rejectsSampling(%q) = %v, want %v", model, got, want)
		}
	}
}
