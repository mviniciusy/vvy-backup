package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// ---------- HDs locais ----------

type Disk struct {
	Mount   string `json:"mount"`
	Mounted bool   `json:"mounted"`
	Total   int64  `json:"total"`
	Used    int64  `json:"used"`
	Parts   []part `json:"parts"`
	Err     string `json:"err"`
}

// refreshDisks mede cada HD usado pelos jobs: quanto entra no backup (rclone size com as exclusões) e o resto.
func (a *App) refreshDisks() {
	// Uma medição por vez, e nunca durante um backup: as duas leem os mesmos HDs.
	if !a.disksBusy.CompareAndSwap(false, true) {
		return
	}
	defer a.disksBusy.Store(false)
	if a.currentRun() != 0 {
		return
	}
	jobs, err := a.jobs()
	if err != nil {
		return
	}
	pm, _ := os.ReadFile("/proc/mounts")
	byMount := map[string][]Job{}
	var mounts []string
	for _, j := range jobs {
		if (j.Kind == "sync" || j.Kind == "copy") && j.Mount != "" {
			if _, ok := byMount[j.Mount]; !ok {
				mounts = append(mounts, j.Mount)
			}
			byMount[j.Mount] = append(byMount[j.Mount], j)
		}
	}
	sort.Strings(mounts)
	var out []Disk
	for _, m := range mounts {
		d := Disk{Mount: m, Mounted: mounted(string(pm), m)}
		if d.Mounted {
			total, free, err := diskUsage(m)
			if err != nil {
				d.Err = err.Error()
			}
			d.Total, d.Used = total, total-free
			var sum int64
			for i, j := range byMount[m] {
				var sz struct{ Bytes int64 }
				params := map[string]any{"fs": j.Src}
				if ex := lines(j.Excludes); len(ex) > 0 {
					params["_filter"] = map[string]any{"ExcludeRule": ex}
				}
				if err := a.rc.Call("operations/size", params, &sz); err != nil {
					d.Err = err.Error()
					continue
				}
				d.Parts = append(d.Parts, part{Name: j.Name, Bytes: sz.Bytes, Color: palette[i%len(palette)], Icon: "hdd",
					Desc: "Entra no backup: " + j.Src + "."})
				sum += sz.Bytes
			}
			if rest := d.Used - sum; rest > 0 {
				d.Parts = append(d.Parts, part{Name: "Fora do backup", Bytes: rest, Color: "#8e8e96", Icon: "folder",
					Desc: "Pastas excluídas ou fora da origem dos jobs."})
			}
		}
		out = append(out, d)
	}
	a.dmu.Lock()
	a.disks = out
	a.dmu.Unlock()
}

func (a *App) disksJSON(w http.ResponseWriter, r *http.Request) {
	a.dmu.Lock()
	ds := a.disks
	a.dmu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ds)
}

// ---------- Mapa de execuções (30 dias) ----------

type cell struct{ Class, Title string }
type heatRow struct {
	Job   Job
	Cells []cell
}

func (a *App) heatmap(jobs []Job, days int) ([]heatRow, []string, error) {
	start := time.Now().AddDate(0, 0, -(days - 1))
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
	rs, err := a.runs(`WHERE r.dry=0 AND r.started>=? ORDER BY r.started`, start.Unix())
	if err != nil {
		return nil, nil, err
	}
	type key struct {
		job int64
		day int
	}
	worst := map[key]Run{}
	for _, r := range rs {
		k := key{r.JobID, dayIndex(start, time.Unix(r.Started, 0))}
		if k.day < 0 || k.day >= days {
			continue
		}
		if cur, ok := worst[k]; !ok || r.Status == "error" || (cur.Status == "ok" && r.Status == "running") {
			worst[k] = r
		}
	}
	var labels []string
	for d := 0; d < days; d++ {
		labels = append(labels, start.AddDate(0, 0, d).Format("02/01"))
	}
	var rows []heatRow
	for _, j := range jobs {
		row := heatRow{Job: j}
		for d := 0; d < days; d++ {
			c := cell{Class: "none", Title: labels[d] + ": não rodou"}
			if r, ok := worst[key{j.ID, d}]; ok {
				c.Class = r.Status
				c.Title = fmt.Sprintf("%s %s: %s", labels[d], time.Unix(r.Started, 0).Format("15:04"), map[string]string{"ok": "OK", "error": "falhou", "running": "rodando"}[r.Status])
				if r.Bytes > 0 {
					c.Title += ", " + humanBytes(r.Bytes)
				}
			}
			row.Cells = append(row.Cells, c)
		}
		rows = append(rows, row)
	}
	return rows, labels, nil
}

// ---------- Próximos 7 dias ----------

type upcoming struct {
	At  time.Time
	Job Job
}
type dayPlan struct {
	Label string
	Items []upcoming
}

func upcomingDays(jobs []Job, days int) []dayPlan {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	plan := make([]dayPlan, days)
	names := []string{"dom", "seg", "ter", "qua", "qui", "sex", "sáb"}
	for d := range plan {
		t := start.AddDate(0, 0, d)
		plan[d].Label = names[t.Weekday()] + " " + t.Format("02")
	}
	end := start.AddDate(0, 0, days)
	for _, j := range jobs {
		if j.Paused || j.Schedule == "" {
			continue
		}
		s, err := cron.ParseStandard(j.Schedule)
		if err != nil {
			continue
		}
		for t := s.Next(now); t.Before(end); t = s.Next(t) {
			if d := dayIndex(start, t); d >= 0 && d < days {
				plan[d].Items = append(plan[d].Items, upcoming{t, j})
			}
		}
	}
	for d := range plan {
		sort.Slice(plan[d].Items, func(x, y int) bool { return plan[d].Items[x].At.Before(plan[d].Items[y].At) })
	}
	return plan
}

// dayIndex conta dias de calendário entre start e t (dias de 23 ou 25 h não deslocam o índice).
func dayIndex(start, t time.Time) int {
	t = t.In(start.Location())
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, start.Location())
	return int(math.Round(day.Sub(start).Hours() / 24))
}

// ---------- Saúde ----------

type issue struct{ Level, Text, Link string }

func (a *App) health(jobs []Job) []issue {
	var out []issue
	if d := a.drive(); d.Err != "" {
		out = append(out, issue{"bad", "Google Drive não respondeu: " + d.Err, ""})
	}
	pm, _ := os.ReadFile("/proc/mounts")
	seen := map[string]bool{}
	for _, j := range jobs {
		if j.Mount != "" && !j.Paused && !seen[j.Mount] {
			seen[j.Mount] = true
			if !mounted(string(pm), j.Mount) {
				out = append(out, issue{"bad", j.Mount + " não está montado. Os jobs desse HD vão falhar.", fmt.Sprintf("/jobs/%d", j.ID)})
			}
		}
	}
	for _, j := range jobs {
		if j.Paused || j.Schedule == "" {
			continue
		}
		s, err := cron.ParseStandard(j.Schedule)
		if err != nil {
			continue
		}
		rs, _ := a.runs(`WHERE r.job_id=? AND r.dry=0 ORDER BY r.started DESC LIMIT 1`, j.ID)
		if len(rs) == 0 {
			continue
		}
		if rs[0].Status == "error" {
			out = append(out, issue{"bad", j.Name + " falhou na última execução.", fmt.Sprintf("/runs/%d", rs[0].ID)})
		}
		if due := s.Next(time.Unix(rs[0].Started, 0)); time.Since(due) > 2*time.Hour {
			out = append(out, issue{"warn", fmt.Sprintf("%s não roda desde %s; devia ter rodado em %s.", j.Name,
				time.Unix(rs[0].Started, 0).Format("02/01"), due.Format("02/01 15:04")), fmt.Sprintf("/jobs/%d", j.ID)})
		}
	}
	a.dmu.Lock()
	v := a.rcVersion
	a.dmu.Unlock()
	if v.old {
		out = append(out, issue{"warn", "rclone " + v.name + " está desatualizado. Versões novas corrigem bugs do Google Drive.", ""})
	}
	return out
}

type rcVer struct {
	name string
	old  bool
}

func (a *App) checkRclone() {
	var v struct {
		Version    string
		Decomposed []int
	}
	if a.rc.Call("core/version", map[string]any{}, &v) != nil || len(v.Decomposed) < 2 {
		return
	}
	a.dmu.Lock()
	a.rcVersion = rcVer{strings.TrimPrefix(v.Version, "v"), v.Decomposed[0] == 1 && v.Decomposed[1] < 65}
	a.dmu.Unlock()
}

// ---------- Crescimento ----------

func (a *App) saveHist(d Drive) {
	var backup int64
	for _, p := range d.Parts {
		if p.Icon != "cloud" && p.Icon != "mail" && !(p.Icon == "trash" && strings.Contains(p.Name, "Google")) {
			backup += p.Bytes
		}
	}
	a.db.Exec(`INSERT OR REPLACE INTO drive_hist(at, used, backup, total) VALUES(?,?,?,?)`, d.At.Unix(), d.Used, backup, d.Total)
}

type growth struct {
	Chart []bar
	Note  string
}

func (a *App) growth() growth {
	var g growth
	rows, err := a.db.Query(`SELECT at, used, backup, total FROM drive_hist WHERE at >= ? ORDER BY at`, time.Now().AddDate(0, 0, -90).Unix())
	if err != nil {
		return g
	}
	defer rows.Close()
	type pt struct{ at, used, backup, total int64 }
	var pts []pt
	perDay := map[string]int{}
	for rows.Next() {
		var p pt
		if rows.Scan(&p.at, &p.used, &p.backup, &p.total) != nil {
			continue
		}
		day := time.Unix(p.at, 0).Format("02/01")
		if i, ok := perDay[day]; ok { // um ponto por dia: o último
			pts[i] = p
			continue
		}
		perDay[day] = len(pts)
		pts = append(pts, p)
	}
	for _, p := range pts {
		g.Chart = append(g.Chart, bar{time.Unix(p.at, 0).Format("02/01"), float64(p.backup), humanBytes(p.backup)})
	}
	if len(pts) < 2 {
		g.Note = "A tendência aparece depois de alguns dias de medições."
		return g
	}
	first, last := pts[0], pts[len(pts)-1]
	days := float64(last.at-first.at) / 86400
	if days < 3 {
		g.Note = "A tendência aparece depois de alguns dias de medições."
		return g
	}
	// O gráfico mostra o backup; a previsão usa a cota inteira, que é o que acaba.
	rate := float64(last.used-first.used) / days
	switch {
	case last.total > 0 && last.used >= last.total:
		g.Note = "A cota do Drive está cheia."
	case rate <= 0:
		g.Note = fmt.Sprintf("Uso da cota estável nos últimos %.0f dias.", days)
	default:
		left := float64(last.total-last.used) / rate
		g.Note = fmt.Sprintf("A cota cresce %s por dia. Nesse ritmo, acaba em %s.", humanBytes(int64(rate)), humanDays(left))
	}
	return g
}

func humanDays(d float64) string {
	switch {
	case d > 730:
		return fmt.Sprintf("%.0f anos", d/365)
	case d > 60:
		return fmt.Sprintf("%.0f meses", d/30)
	}
	return fmt.Sprintf("%.0f dias", d)
}

// ---------- Execução em andamento ----------

func (a *App) runningJSON(w http.ResponseWriter, r *http.Request) {
	id := a.currentRun()
	a.mu.Lock()
	live := a.live[id]
	a.mu.Unlock()
	out := map[string]any{"id": id}
	if id != 0 {
		if rs, err := a.runs(`WHERE r.id=?`, id); err == nil && len(rs) > 0 {
			out["job"], out["dry"] = rs[0].JobName, rs[0].Dry
		}
		if live != nil {
			out["bytes"], out["total"], out["speed"] = live.Bytes, live.TotalBytes, live.Speed
			if len(live.Transferring) > 0 {
				out["file"] = path.Base(live.Transferring[0].Name)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
