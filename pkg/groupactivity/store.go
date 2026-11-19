// Package groupactivity counts WhatsApp group messages per member per day (WIB). It stores no message content:
// only (group, sender, day, count) and the sender's last push name.
package groupactivity

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// WIB is the zone days are bucketed in.
var WIB = time.FixedZone("WIB", 7*3600)

const dayFormat = "2006-01-02"

// Day formats t as the YYYY-MM-DD bucket (WIB).
func Day(t time.Time) string { return t.In(WIB).Format(dayFormat) }

type Store struct {
	db        *sql.DB
	retention int // days; <=0 keeps everything
}

// Open opens (creating if needed) the SQLite file at path and purges rows older than retentionDays.
func Open(path string, retentionDays int) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS group_activity(
		chat TEXT NOT NULL, sender TEXT NOT NULL, day TEXT NOT NULL, n INTEGER NOT NULL,
		push_name TEXT NOT NULL DEFAULT '', PRIMARY KEY(chat, sender, day))`)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("group_activity: %w", err)
	}
	s := &Store{db: db, retention: retentionDays}
	if err := s.Purge(time.Now()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Record adds one message from sender in chat on t's day, remembering the latest non-empty push name.
func (s *Store) Record(chat, sender, pushName string, t time.Time) error {
	_, err := s.db.Exec(`INSERT INTO group_activity(chat, sender, day, n, push_name) VALUES(?,?,?,1,?)
		ON CONFLICT(chat, sender, day) DO UPDATE SET n = n + 1,
		push_name = CASE WHEN excluded.push_name <> '' THEN excluded.push_name ELSE push_name END`,
		chat, sender, Day(t), pushName)
	return err
}

// Purge deletes rows older than the retention window.
func (s *Store) Purge(now time.Time) error {
	if s.retention <= 0 {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM group_activity WHERE day < ?`, Day(now.AddDate(0, 0, -s.retention)))
	return err
}

// Run purges once a day until ctx ends.
func (s *Store) Run(ctx context.Context) {
	tk := time.NewTicker(24 * time.Hour)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tk.C:
			_ = s.Purge(now)
		}
	}
}

type Member struct {
	Sender   string
	PushName string
	N        int
}

type Summary struct {
	Members []Member // most messages first, at most the requested limit
	Total   int      // messages across all members in the period
	People  int      // members with at least one message in the period
	Since   string   // first day with data for this chat (YYYY-MM-DD), "" when none
}

// Top returns the busiest members of chat from day `from` (inclusive) on.
func (s *Store) Top(chat, from string, limit int) (Summary, error) {
	var sum Summary
	rows, err := s.db.Query(`SELECT sender, COALESCE((SELECT push_name FROM group_activity g2
			WHERE g2.chat = g.chat AND g2.sender = g.sender AND g2.push_name <> '' ORDER BY day DESC LIMIT 1), ''), SUM(n) AS t
		FROM group_activity g WHERE chat = ? AND day >= ? GROUP BY sender ORDER BY t DESC, sender`, chat, from)
	if err != nil {
		return sum, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.Sender, &m.PushName, &m.N); err != nil {
			return sum, err
		}
		sum.Total += m.N
		sum.People++
		if len(sum.Members) < limit {
			sum.Members = append(sum.Members, m)
		}
	}
	if err := rows.Err(); err != nil {
		return sum, err
	}
	var first sql.NullString
	if err := s.db.QueryRow(`SELECT MIN(day) FROM group_activity WHERE chat = ?`, chat).Scan(&first); err != nil {
		return sum, err
	}
	sum.Since = first.String
	return sum, nil
}
