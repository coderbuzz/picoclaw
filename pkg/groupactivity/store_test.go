package groupactivity

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreCountsPerDayAndPurges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ga.db")
	s, err := Open(path, 90)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, WIB)
	old := now.AddDate(0, 0, -100)
	_ = s.Record("g1", "a@lid", "Ani", now)
	_ = s.Record("g1", "a@lid", "", now) // empty push name keeps the old one
	_ = s.Record("g1", "b@lid", "Budi", now.AddDate(0, 0, -3))
	_ = s.Record("g1", "c@lid", "Cici", old)
	_ = s.Record("g2", "a@lid", "Ani", now)
	if err := s.Purge(now); err != nil {
		t.Fatal(err)
	}

	week, _ := s.Top("g1", Day(now.AddDate(0, 0, -6)), 30)
	if week.Total != 3 || week.People != 2 || week.Members[0].Sender != "a@lid" || week.Members[0].N != 2 || week.Members[0].PushName != "Ani" {
		t.Errorf("week = %+v", week)
	}
	if today, _ := s.Top("g1", Day(now), 30); today.Total != 2 || today.People != 1 {
		t.Errorf("today = %+v", today)
	}
	if one, _ := s.Top("g1", "2000-01-01", 1); len(one.Members) != 1 || one.People != 2 {
		t.Errorf("limit = %+v", one)
	}
	if week.Since != Day(now.AddDate(0, 0, -3)) {
		t.Errorf("since = %q (rows older than retention must be gone)", week.Since)
	}
	if none, _ := s.Top("g3", "2000-01-01", 30); none.Total != 0 || none.Since != "" {
		t.Errorf("other chat = %+v", none)
	}
}

func TestDayIsWIB(t *testing.T) {
	if d := Day(time.Date(2026, 11, 18, 18, 0, 0, 0, time.UTC)); d != "2026-11-19" {
		t.Errorf("18:00 UTC = %s, want next WIB day", d)
	}
}
