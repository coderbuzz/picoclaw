package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
)

const panggilanHeader = `# Daftar Panggilan & Gender Anggota Grup

Dipakai bersama oleh ` + "`alumni`" + `, ` + "`alumni-admin`" + `, dan ` + "`alumni-staff`" + `. Kunci utama: ` + "`ID`" + ` pengirim dari WhatsApp (` + "`Current sender: ... (ID: <ID>)`" + `, kutipan ` + "`[quoted ... message from <ID>]`" + `, atau mention ` + "`@<ID>`" + `). Tanpa nomor telepon bila memakai LID. Perbarui baris yang sudah ada jika ` + "`ID`" + ` sama.

| ID | Nama / Layar | Gender | Panggilan | Catatan |
|---|---|---|---|---|
`

var panggilanFileMu sync.Mutex

type panggilanEntry struct {
	ID        string
	Nama      string
	Gender    string
	Panggilan string
	Catatan   string
}

// PanggilanTool reads and updates a shared Markdown table of member callnames and genders keyed by sender ID.
// Ordinary member agents (CanSetAny=false) may only update their own SenderID from the turn context.
type PanggilanTool struct {
	cfg      *config.AgentPanggilanConfig
	filePath string
}

func NewPanggilanTool(cfg *config.AgentPanggilanConfig, workspace string) *PanggilanTool {
	file := ""
	if cfg != nil {
		file = strings.TrimSpace(cfg.File)
	}
	if file == "" {
		file = "panggilan.md"
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(workspace, file)
	}
	return &PanggilanTool{cfg: cfg, filePath: file}
}

func (t *PanggilanTool) Name() string { return "panggilan" }

func (t *PanggilanTool) Description() string {
	return "Read or save a group member's preferred callname (panggilan) and gender by WhatsApp sender ID. " +
		"action=get: look up an ID (defaults to current sender); action=set: save or update panggilan/gender/nama/catatan " +
		"(ordinary members can only set their own caller ID); action=delete: remove an entry."
}

func (t *PanggilanTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":    map[string]any{"type": "string", "enum": []string{"get", "set", "delete"}},
			"target_id": map[string]any{"type": "string", "description": "Optional WhatsApp sender ID (e.g. 12345@lid); defaults to current sender. Only admin/staff may specify another member's ID."},
			"panggilan": map[string]any{"type": "string", "description": "Preferred callname, e.g. 'Mbak Rahayu', 'Pak Budi', 'Budhe Tin' (required for set)."},
			"gender":    map[string]any{"type": "string", "enum": []string{"L", "P", "-"}, "description": "Optional gender: L, P, or -."},
			"nama":      map[string]any{"type": "string", "description": "Optional display/full name."},
			"catatan":   map[string]any{"type": "string", "description": "Optional short note."},
		},
		"required": []string{"action"},
	}
}

func (t *PanggilanTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	action, _ := args["action"].(string)
	senderID := strings.TrimSpace(ToolSenderID(ctx))
	targetID, _ := args["target_id"].(string)
	targetID = sanitizeCell(targetID, 80)

	switch action {
	case "get":
		id := targetID
		if id == "" {
			id = senderID
		}
		if id == "" {
			return ErrorResult("panggilan: ID pengirim tidak tersedia")
		}
		panggilanFileMu.Lock()
		defer panggilanFileMu.Unlock()
		entries, _, err := t.load()
		if err != nil {
			return ErrorResult("panggilan: gagal membaca file: " + err.Error())
		}
		for _, e := range entries {
			if e.ID == id {
				return NewToolResult(fmt.Sprintf("ID: %s | Nama: %s | Gender: %s | Panggilan: %s | Catatan: %s",
					e.ID, e.Nama, e.Gender, e.Panggilan, e.Catatan))
			}
		}
		return NewToolResult("Belum tercatat untuk ID: " + id)

	case "set":
		id, errMsg := t.resolveWritableID(senderID, targetID)
		if errMsg != "" {
			return ErrorResult(errMsg)
		}
		rawPanggilan, _ := args["panggilan"].(string)
		callname := sanitizeCell(rawPanggilan, 40)
		if callname == "" {
			return ErrorResult("panggilan: parameter 'panggilan' wajib diisi")
		}
		rawGender, hasGender := args["gender"].(string)
		gender := normalizeGender(rawGender)
		rawNama, hasNama := args["nama"].(string)
		nama := sanitizeCell(rawNama, 60)
		rawCatatan, hasCatatan := args["catatan"].(string)
		catatan := sanitizeCell(rawCatatan, 80)

		panggilanFileMu.Lock()
		defer panggilanFileMu.Unlock()
		entries, preamble, err := t.load()
		if err != nil {
			return ErrorResult("panggilan: gagal membaca file: " + err.Error())
		}
		updated := false
		for i := range entries {
			if entries[i].ID == id {
				entries[i].Panggilan = callname
				if hasGender && strings.TrimSpace(rawGender) != "" {
					entries[i].Gender = gender
				}
				if hasNama && nama != "" {
					entries[i].Nama = nama
				}
				if hasCatatan {
					entries[i].Catatan = catatan
				}
				updated = true
				break
			}
		}
		if !updated {
			if nama == "" {
				nama = "-"
			}
			entries = append(entries, panggilanEntry{
				ID:        id,
				Nama:      nama,
				Gender:    gender,
				Panggilan: callname,
				Catatan:   catatan,
			})
		}
		if err := t.save(preamble, entries); err != nil {
			return ErrorResult("panggilan: gagal menyimpan file: " + err.Error())
		}
		return NewToolResult(fmt.Sprintf("Tersimpan: %s -> %s (gender: %s)", id, callname, gender))

	case "delete":
		id, errMsg := t.resolveWritableID(senderID, targetID)
		if errMsg != "" {
			return ErrorResult(errMsg)
		}
		panggilanFileMu.Lock()
		defer panggilanFileMu.Unlock()
		entries, preamble, err := t.load()
		if err != nil {
			return ErrorResult("panggilan: gagal membaca file: " + err.Error())
		}
		filtered := entries[:0]
		removed := false
		for _, e := range entries {
			if e.ID == id {
				removed = true
				continue
			}
			filtered = append(filtered, e)
		}
		if !removed {
			return NewToolResult("Belum tercatat untuk ID: " + id)
		}
		if err := t.save(preamble, filtered); err != nil {
			return ErrorResult("panggilan: gagal menyimpan file: " + err.Error())
		}
		return NewToolResult("Dihapus untuk ID: " + id)

	default:
		return ErrorResult("panggilan: action harus get, set, atau delete")
	}
}

func (t *PanggilanTool) resolveWritableID(senderID, targetID string) (string, string) {
	canSetAny := t.cfg != nil && t.cfg.CanSetAny
	if targetID != "" && targetID != senderID && !canSetAny {
		return "", "Hanya admin/staf yang bisa mengubah panggilan orang lain; anggota hanya bisa mengatur panggilan sendiri."
	}
	id := targetID
	if id == "" {
		id = senderID
	}
	if id == "" {
		return "", "panggilan: ID pengirim tidak tersedia"
	}
	return id, ""
}

func (t *PanggilanTool) load() ([]panggilanEntry, string, error) {
	data, err := os.ReadFile(t.filePath)
	if os.IsNotExist(err) {
		return nil, panggilanHeader, nil
	}
	if err != nil {
		return nil, "", err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var preambleLines []string
	var entries []panggilanEntry
	inTable := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inTable {
			preambleLines = append(preambleLines, line)
			if strings.HasPrefix(trimmed, "|---") {
				inTable = true
			}
			continue
		}
		if trimmed == "" || !strings.HasPrefix(trimmed, "|") {
			continue
		}
		parts := strings.Split(strings.Trim(trimmed, "|"), "|")
		if len(parts) < 4 {
			continue
		}
		var catatan string
		if len(parts) >= 5 {
			catatan = strings.TrimSpace(parts[4])
		}
		id := strings.TrimSpace(parts[0])
		if id == "" {
			continue
		}
		entries = append(entries, panggilanEntry{
			ID:        id,
			Nama:      strings.TrimSpace(parts[1]),
			Gender:    normalizeGender(parts[2]),
			Panggilan: strings.TrimSpace(parts[3]),
			Catatan:   catatan,
		})
	}
	preamble := panggilanHeader
	if inTable {
		preamble = strings.Join(preambleLines, "\n") + "\n"
	}
	return entries, preamble, nil
}

func (t *PanggilanTool) save(preamble string, entries []panggilanEntry) error {
	if err := os.MkdirAll(filepath.Dir(t.filePath), 0o700); err != nil {
		return err
	}
	var sb strings.Builder
	sb.WriteString(preamble)
	for _, e := range entries {
		fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s |\n",
			sanitizeCell(e.ID, 80),
			sanitizeCell(e.Nama, 60),
			normalizeGender(e.Gender),
			sanitizeCell(e.Panggilan, 40),
			sanitizeCell(e.Catatan, 80),
		)
	}
	tmp := t.filePath + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, t.filePath)
}

func sanitizeCell(s string, maxRunes int) string {
	s = strings.NewReplacer("\r", " ", "\n", " ", "|", "/").Replace(s)
	s = strings.TrimSpace(s)
	if maxRunes > 0 && utf8.RuneCountInString(s) > maxRunes {
		runes := []rune(s)
		s = strings.TrimSpace(string(runes[:maxRunes]))
	}
	return s
}

func normalizeGender(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "L", "LAKI", "LAKI-LAKI", "M", "MALE", "PRIA":
		return "L"
	case "P", "PEREMPUAN", "F", "FEMALE", "WANITA":
		return "P"
	default:
		return "-"
	}
}
