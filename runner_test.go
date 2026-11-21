package main

import (
	"strings"
	"testing"
	"time"
)

func TestSyncStep(t *testing.T) {
	now := time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC)
	s := syncStep(Job{Kind: "sync", Src: "/mnt/a", Dst: "gdrive:x", Trash: "gdrive:lix/A/", Excludes: "/vzdump/**\n\n"}, "run/1", true, now)
	if s.path != "sync/sync" {
		t.Fatalf("sync: %+v", s)
	}
	cfg := s.params["_config"].(map[string]any)
	if cfg["BackupDir"] != "gdrive:lix/A/2026-11-21" || cfg["DryRun"] != true {
		t.Errorf("config: %v", cfg)
	}
	if ex := s.params["_filter"].(map[string]any)["ExcludeRule"].([]string); len(ex) != 1 {
		t.Errorf("exclude: %v", ex)
	}
	if c := syncStep(Job{Kind: "copy", Src: "/a", Dst: "g:b", Trash: "g:l"}, "g", false, now); c.path != "copy/copy" ||
		c.params["_config"].(map[string]any)["BackupDir"] != nil {
		t.Errorf("copy não usa lixeira: %+v", c)
	}
}

func TestTrash(t *testing.T) {
	if n, err := trashDays("14d"); n != 14 || err != nil {
		t.Errorf("14d: %d %v", n, err)
	}
	for _, bad := range []string{"", "0", "2w", "abc"} {
		if _, err := trashDays(bad); err == nil {
			t.Errorf("%q deveria falhar", bad)
		}
	}
	cutoff := time.Date(2026, 11, 7, 15, 0, 0, 0, time.Local)
	got := oldTrashDirs([]string{"HD-A", "HD-A/2026-11-06", "HD-A/2026-11-07", "HD-B/2026-10-01", "2026-11-01", "HD-A/x"}, cutoff)
	want := "HD-A/2026-11-06,HD-B/2026-10-01,2026-11-01"
	if strings.Join(got, ",") != want {
		t.Errorf("oldTrashDirs = %v, quero %s", got, want)
	}
}

func TestScheduleBounds(t *testing.T) {
	for _, c := range []string{"0 3 * * 7", "0 25 * * *", "99 1 * * *", "0 3 31 * *"} {
		if p := parseSchedule(c); p.Freq != "custom" {
			t.Errorf("%q deveria ser custom: %+v", c, p)
		}
		humanSchedule(c) // não pode entrar em pânico
	}
	if _, err := buildSchedule("custom", "", "", "", "@every 1s"); err == nil {
		t.Error("@every deveria ser recusado")
	}
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local)
	if d := dayIndex(start, start.AddDate(0, 0, 6).Add(23*time.Hour+30*time.Minute)); d != 6 {
		t.Errorf("dayIndex = %d", d)
	}
}

func TestMounted(t *testing.T) {
	pm := "/dev/sdb1 /mnt/hd1 fuseblk rw 0 0\n/dev/sdc1 /mnt/HD\\040WD ext4 rw 0 0\n"
	for path, want := range map[string]bool{"/mnt/hd1": true, "/mnt/hd1/": true, "/mnt/HD WD": true, "/mnt": false} {
		if mounted(pm, path) != want {
			t.Errorf("mounted(%q) != %v", path, want)
		}
	}
}

func TestSchedule(t *testing.T) {
	for _, c := range []struct{ freq, dow, dom, tm, want string }{
		{"daily", "", "", "02:00", "0 2 * * *"},
		{"weekly", "1", "", "03:00", "0 3 * * 1"},
		{"monthly", "", "8", "01:30", "30 1 8 * *"},
		{"none", "", "", "", ""},
	} {
		got, err := buildSchedule(c.freq, c.dow, c.dom, c.tm, "")
		if err != nil || got != c.want {
			t.Errorf("%v: %q %v", c, got, err)
		}
		back := parseSchedule(got)
		if back.Freq != c.freq || (c.freq != "none" && back.Time != c.tm) {
			t.Errorf("ida e volta %q: %+v", got, back)
		}
	}
	if p := parseSchedule("30 1 8-14 * 0"); p.Freq != "custom" {
		t.Errorf("custom: %+v", p)
	}
	if _, err := buildSchedule("monthly", "", "31", "03:00", ""); err == nil {
		t.Error("dia 31 deveria falhar")
	}
}

func TestUpcomingDays(t *testing.T) {
	jobs := []Job{
		{ID: 1, Name: "diário", Schedule: "0 2 * * *"},
		{ID: 2, Name: "pausado", Schedule: "0 2 * * *", Paused: true},
		{ID: 3, Name: "sem agenda"},
	}
	plan := upcomingDays(jobs, 7)
	n := 0
	for _, d := range plan {
		for _, it := range d.Items {
			if it.Job.ID != 1 {
				t.Errorf("job inesperado: %s", it.Job.Name)
			}
			n++
		}
	}
	if len(plan) != 7 || n < 6 || n > 7 {
		t.Errorf("dias=%d execuções=%d", len(plan), n)
	}
	if humanDays(400) != "13 meses" || humanDays(800) != "2 anos" || humanDays(10) != "10 dias" {
		t.Error("humanDays")
	}
}
