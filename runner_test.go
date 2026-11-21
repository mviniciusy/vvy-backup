package main

import (
	"testing"
	"time"
)

func TestSteps(t *testing.T) {
	now := time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC)

	s := steps(Job{Kind: "sync", Src: "/mnt/a", Dst: "gdrive:x", Trash: "gdrive:lix/A/", Excludes: "/vzdump/**\n\n"}, "run/1", true, now)
	if len(s) != 1 || s[0].path != "sync/sync" {
		t.Fatalf("sync: %+v", s)
	}
	cfg := s[0].params["_config"].(map[string]any)
	if cfg["BackupDir"] != "gdrive:lix/A/2026-11-21" || cfg["DryRun"] != true {
		t.Errorf("config: %v", cfg)
	}
	if ex := s[0].params["_filter"].(map[string]any)["ExcludeRule"].([]string); len(ex) != 1 {
		t.Errorf("exclude: %v", ex)
	}

	if s := steps(Job{Kind: "copy", Src: "/a", Dst: "g:b", Trash: "g:l"}, "g", false, now); s[0].path != "copy/copy" ||
		s[0].params["_config"].(map[string]any)["BackupDir"] != nil {
		t.Errorf("copy não usa lixeira: %+v", s)
	}

	c := steps(Job{Kind: "clean", Dst: "g:lix", MinAge: "14d"}, "g", false, now)
	if len(c) != 2 || c[0].path != "operations/delete" || c[0].params["_filter"].(map[string]any)["MinAge"] != "14d" ||
		len(c[1].params["_filter"].(map[string]any)) != 0 {
		t.Errorf("clean: %+v", c)
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
