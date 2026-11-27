package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContextBuilder_CurrentSenderIncludesPanggilan(t *testing.T) {
	ws := t.TempDir()
	panggilanPath := filepath.Join(ws, "panggilan.md")
	content := `# Daftar Panggilan
| ID | Nama / Layar | Gender | Panggilan | Catatan |
|---|---|---|---|---|
| 198719580450859@lid | Wihastina Wulandari | P | Mbak Wihastina | |
`
	if err := os.WriteFile(panggilanPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cb := NewContextBuilder(ws).WithPanggilanFile(panggilanPath)

	msgs := cb.BuildMessages(nil, "", "halo", nil, "whatsapp", "123@g.us", "198719580450859@lid", "Wihastina Wulandari")
	joined := ""
	for _, m := range msgs {
		joined += m.Content + "\n"
	}
	if !strings.Contains(joined, "Panggilan: Mbak Wihastina (Gender: P)") {
		t.Fatalf("expected injected panggilan in Current Sender, got:\n%s", joined)
	}

	// Unknown sender should not inject a Panggilan line.
	msgs2 := cb.BuildMessages(nil, "", "halo", nil, "whatsapp", "123@g.us", "999@lid", "Unknown")
	joined2 := ""
	for _, m := range msgs2 {
		joined2 += m.Content + "\n"
	}
	if strings.Contains(joined2, "Panggilan:") {
		t.Fatalf("did not expect Panggilan line for unknown sender, got:\n%s", joined2)
	}
}
