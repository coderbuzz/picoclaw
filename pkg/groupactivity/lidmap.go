package groupactivity

import (
	"database/sql"
	"strings"
)

// PhoneNumbers maps senders to phone-number digits using the whatsmeow session store (storeDB = store.db): a PN JID
// carries its number, a LID is looked up in whatsmeow_lid_map. Unknown senders are absent from the result.
func PhoneNumbers(storeDB string, senders []string) map[string]string {
	out := map[string]string{}
	var lids []string
	for _, s := range senders {
		user, server, _ := strings.Cut(s, "@")
		switch server {
		case "s.whatsapp.net":
			out[s] = user
		case "lid":
			lids = append(lids, s)
		}
	}
	if len(lids) == 0 {
		return out
	}
	db, err := sql.Open("sqlite", "file:"+storeDB+"?mode=ro&_pragma=busy_timeout(3000)")
	if err != nil {
		return out
	}
	defer db.Close()
	for _, s := range lids {
		user, _, _ := strings.Cut(s, "@")
		var pn string
		if db.QueryRow(`SELECT pn FROM whatsmeow_lid_map WHERE lid = ?`, user).Scan(&pn) == nil && pn != "" {
			out[s] = pn
		}
	}
	return out
}
