package main

import (
	"crypto/subtle"
	"database/sql"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

//go:embed web
var webFS embed.FS

var pages = map[string]*template.Template{}

func init() {
	for _, p := range []string{"painel", "job", "run"} {
		pages[p] = template.Must(template.New("").Funcs(funcs).ParseFS(webFS, "web/layout.html", "web/"+p+".html"))
	}
}

var funcs = template.FuncMap{
	"bytes": humanBytes,
	"when": func(t int64) string {
		if t == 0 {
			return "—"
		}
		return time.Unix(t, 0).Format("02/01 15:04")
	},
	"dur": func(a, b int64) string {
		if b == 0 {
			b = time.Now().Unix()
		}
		return (time.Duration(b-a) * time.Second).String()
	},
	"pct": func(a, b int64) int64 {
		if b == 0 {
			return 0
		}
		return a * 100 / b
	},
	"status": func(s string) string {
		return map[string]string{"ok": "OK", "error": "Falhou", "running": "Rodando"}[s]
	},
	"bars": bars,
	"rate": func(v float64) string { return humanBytes(int64(v)) + "/s" },
	"list": func(s ...string) []string { return s },
	"seg":  segStyle,
	"dot":  dotStyle,
	"sub":  func(a, b int64) int64 { return a - b },
}

func humanBytes(n int64) string {
	f := float64(n)
	for _, u := range []string{"B", "KB", "MB", "GB", "TB"} {
		if f < 1024 || u == "TB" {
			if u == "B" {
				return fmt.Sprintf("%d B", n)
			}
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return ""
}

type bar struct {
	Label string
	V     float64
	Text  string
}

// bars desenha um gráfico de barras em SVG, rotulando só o maior valor.
func bars(bs []bar) template.HTML {
	if len(bs) == 0 {
		return ""
	}
	var max float64
	maxI := 0
	for i, b := range bs {
		if b.V > max {
			max, maxI = b.V, i
		}
	}
	if max == 0 {
		return ""
	}
	const h, w = 80.0, 12.0
	var sb strings.Builder
	fmt.Fprintf(&sb, `<svg class="chart" viewBox="0 0 %.0f %.0f" preserveAspectRatio="none" role="img">`, float64(len(bs))*w, h+14)
	fmt.Fprintf(&sb, `<line x1="0" y1="%.0f" x2="%.0f" y2="%.0f" class="base"/>`, h, float64(len(bs))*w, h)
	for i, b := range bs {
		bh := 0.0
		if max > 0 {
			bh = b.V / max * h
		}
		fmt.Fprintf(&sb, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f"><title>%s: %s</title></rect>`,
			float64(i)*w+2, h-bh, w-4, bh, template.HTMLEscapeString(b.Label), template.HTMLEscapeString(b.Text))
	}
	if max > 0 {
		fmt.Fprintf(&sb, `<text x="%.1f" y="%.0f">%s</text>`, float64(maxI)*w+w/2, h+12, template.HTMLEscapeString(bs[maxI].Text))
	}
	sb.WriteString(`</svg>`)
	return template.HTML(sb.String())
}

func basicAuth(user, pass string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(u), []byte(user)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(pass)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="vvy-backup"`)
			http.Error(w, "Acesso negado.", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) routes() http.Handler {
	m := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web/static")
	m.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	m.HandleFunc("GET /{$}", a.painel)
	m.HandleFunc("GET /jobs/new", a.jobPage)
	m.HandleFunc("GET /jobs/{id}", a.jobPage)
	m.HandleFunc("POST /jobs", a.saveJob)
	m.HandleFunc("POST /jobs/{id}", a.saveJob)
	m.HandleFunc("POST /jobs/{id}/run", a.runJob)
	m.HandleFunc("POST /jobs/{id}/dry", a.runJob)
	m.HandleFunc("POST /jobs/{id}/pause", a.pauseJob)
	m.HandleFunc("GET /runs/{id}", a.runPage)
	m.HandleFunc("GET /runs/{id}/live", a.runPage)
	m.HandleFunc("POST /runs/{id}/stop", a.stopRun)
	m.HandleFunc("POST /api/report", a.report)
	m.HandleFunc("GET /dirs", a.dirs)
	m.HandleFunc("POST /drive/refresh", func(w http.ResponseWriter, r *http.Request) {
		go a.refreshDrive()
		time.Sleep(300 * time.Millisecond)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	return m
}

type dirEntry struct {
	Name, Rel string
	Kids      bool
}

// dirs lista as subpastas de src/rel (só dentro de /mnt) para a árvore do formulário.
func (a *App) dirs(w http.ResponseWriter, r *http.Request) {
	base := path.Clean(r.URL.Query().Get("src"))
	rel := path.Clean("/" + r.URL.Query().Get("rel"))
	full := path.Join(base, rel)
	if !strings.HasPrefix(base, "/mnt/") {
		http.Error(w, "A origem precisa estar em /mnt.", http.StatusBadRequest)
		return
	}
	ents, err := os.ReadDir(full)
	if err != nil {
		http.Error(w, "Pasta não encontrada.", http.StatusNotFound)
		return
	}
	var out []dirEntry
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, dirEntry{e.Name(), path.Join(rel, e.Name()), hasSubdir(path.Join(full, e.Name()))})
		}
	}
	render(w, "job", "dirs", out)
}

func hasSubdir(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	for {
		ents, err := f.ReadDir(64)
		for _, e := range ents {
			if e.IsDir() {
				return true
			}
		}
		if err != nil {
			return false
		}
	}
}

// report recebe o resultado de um job do host, enviado pelo wrapper bm-run.
func (a *App) report(w http.ResponseWriter, r *http.Request) {
	f := r.PostFormValue
	started, err1 := strconv.ParseInt(f("started"), 10, 64)
	finished, err2 := strconv.ParseInt(f("finished"), 10, 64)
	code, err3 := strconv.Atoi(f("code"))
	if err1 != nil || err2 != nil || err3 != nil {
		http.Error(w, "started, finished e code são obrigatórios", http.StatusBadRequest)
		return
	}
	var jobID int64
	if err := a.db.QueryRow(`SELECT id FROM jobs WHERE name=? AND kind='host'`, f("name")).Scan(&jobID); err != nil {
		http.Error(w, "job do host não cadastrado: "+f("name"), http.StatusNotFound)
		return
	}
	status := "ok"
	if code != 0 {
		status = "error"
	}
	if _, err := a.db.Exec(`INSERT INTO runs(job_id,started,finished,status,errors,msg) VALUES(?,?,?,?,?,?)`,
		jobID, started, finished, status, min(code, 1), f("msg")); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func render(w http.ResponseWriter, page, name string, data any) {
	if err := pages[page].ExecuteTemplate(w, name, data); err != nil {
		log.Printf("template %s: %v", page, err)
	}
}

func fail(w http.ResponseWriter, err error) {
	log.Print(err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

type Run struct {
	ID, JobID, Started, Finished  int64
	Status                        string
	Dry                           bool
	Bytes, Files, Deletes, Errors int64
	Msg, JobName                  string
}

const runCols = `r.id,r.job_id,r.started,coalesce(r.finished,0),r.status,r.dry,r.bytes,r.files,r.deletes,r.errors,r.msg,j.name`

func scanRun(s interface{ Scan(...any) error }) (Run, error) {
	var r Run
	err := s.Scan(&r.ID, &r.JobID, &r.Started, &r.Finished, &r.Status, &r.Dry, &r.Bytes, &r.Files, &r.Deletes, &r.Errors, &r.Msg, &r.JobName)
	return r, err
}

func (a *App) runs(where string, args ...any) ([]Run, error) {
	rows, err := a.db.Query(`SELECT `+runCols+` FROM runs r JOIN jobs j ON j.id=r.job_id `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (a *App) next(j Job) int64 {
	if j.Kind == "host" {
		if s, err := cron.ParseStandard(j.Schedule); err == nil && j.Schedule != "" {
			return s.Next(time.Now()).Unix()
		}
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if id, ok := a.entries[j.ID]; ok {
		return a.cron.Entry(id).Next.Unix()
	}
	return 0
}

func (a *App) painel(w http.ResponseWriter, r *http.Request) {
	jobs, err := a.jobs()
	if err != nil {
		fail(w, err)
		return
	}
	type row struct {
		Job  Job
		Last *Run
		Next int64
	}
	var rows []row
	for _, j := range jobs {
		rs, err := a.runs(`WHERE r.job_id=? AND r.dry=0 ORDER BY r.started DESC LIMIT 1`, j.ID)
		if err != nil {
			fail(w, err)
			return
		}
		x := row{Job: j, Next: a.next(j)}
		if len(rs) > 0 {
			x.Last = &rs[0]
		}
		rows = append(rows, x)
	}

	// Volume enviado por dia, últimos 30 dias.
	since := time.Now().AddDate(0, 0, -29)
	since = time.Date(since.Year(), since.Month(), since.Day(), 0, 0, 0, 0, time.Local)
	perDay := map[string]int64{}
	rs, err := a.runs(`WHERE r.dry=0 AND r.started>=?`, since.Unix())
	if err != nil {
		fail(w, err)
		return
	}
	for _, x := range rs {
		perDay[time.Unix(x.Started, 0).Format("02/01")] += x.Bytes
	}
	var chart []bar
	for d := since; d.Before(time.Now()); d = d.AddDate(0, 0, 1) {
		k := d.Format("02/01")
		chart = append(chart, bar{k, float64(perDay[k]), humanBytes(perDay[k])})
	}

	a.mu.Lock()
	running := a.running
	a.mu.Unlock()
	var trashJob *Job
	for i := range jobs {
		if jobs[i].Kind == "clean" {
			trashJob = &jobs[i]
		}
	}
	render(w, "painel", "layout", map[string]any{"Rows": rows, "Chart": chart, "Running": running,
		"Drive": a.drive(), "TrashJob": trashJob})
}

func (a *App) jobPage(w http.ResponseWriter, r *http.Request) {
	j := Job{Kind: "sync", Transfers: 4, Checkers: 8}
	var hist []Run
	var vol, dur []bar
	if id := pathID(r); id != 0 {
		var err error
		if j, err = a.job(id); err == sql.ErrNoRows {
			http.NotFound(w, r)
			return
		} else if err != nil {
			fail(w, err)
			return
		}
		if hist, err = a.runs(`WHERE r.job_id=? ORDER BY r.started DESC LIMIT 50`, id); err != nil {
			fail(w, err)
			return
		}
		for i := len(hist) - 1; i >= 0; i-- {
			h := hist[i]
			if h.Dry || h.Finished == 0 {
				continue
			}
			label := time.Unix(h.Started, 0).Format("02/01")
			vol = append(vol, bar{label, float64(h.Bytes), humanBytes(h.Bytes)})
			d := time.Duration(h.Finished-h.Started) * time.Second
			dur = append(dur, bar{label, d.Seconds(), d.String()})
		}
	}
	render(w, "job", "layout", map[string]any{
		"Job": j, "Runs": hist, "Vol": vol, "Dur": dur, "Next": a.next(j), "Err": r.URL.Query().Get("err"),
		"S": parseSchedule(j.Schedule),
	})
}

// Agenda amigável <-> cron. Só os formatos que o formulário gera; o resto vira "custom".
type sched struct{ Freq, Dow, Dom, Time string }

func parseSchedule(s string) sched {
	f := strings.Fields(s)
	if len(f) == 0 {
		return sched{Freq: "none", Time: "03:00"}
	}
	num := func(x string) (int, bool) { n, err := strconv.Atoi(x); return n, err == nil }
	if len(f) == 5 && f[3] == "*" {
		m, ok1 := num(f[0])
		h, ok2 := num(f[1])
		if ok1 && ok2 {
			t := fmt.Sprintf("%02d:%02d", h, m)
			_, dowOK := num(f[4])
			_, domOK := num(f[2])
			switch {
			case f[2] == "*" && f[4] == "*":
				return sched{Freq: "daily", Time: t}
			case f[2] == "*" && dowOK:
				return sched{Freq: "weekly", Dow: f[4], Time: t}
			case domOK && f[4] == "*":
				return sched{Freq: "monthly", Dom: f[2], Time: t}
			}
		}
	}
	return sched{Freq: "custom", Time: "03:00"}
}

func buildSchedule(freq, dow, dom, hhmm, raw string) (string, error) {
	if freq == "none" {
		return "", nil
	}
	if freq == "custom" {
		raw = strings.TrimSpace(raw)
		_, err := cron.ParseStandard(raw)
		return raw, err
	}
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return "", fmt.Errorf("horário inválido")
	}
	m, h := t.Minute(), t.Hour()
	switch freq {
	case "daily":
		return fmt.Sprintf("%d %d * * *", m, h), nil
	case "weekly":
		if d, err := strconv.Atoi(dow); err != nil || d < 0 || d > 6 {
			return "", fmt.Errorf("dia da semana inválido")
		}
		return fmt.Sprintf("%d %d * * %s", m, h, dow), nil
	case "monthly":
		if d, err := strconv.Atoi(dom); err != nil || d < 1 || d > 28 {
			return "", fmt.Errorf("dia do mês deve ser de 1 a 28")
		}
		return fmt.Sprintf("%d %d %s * *", m, h, dom), nil
	}
	return "", fmt.Errorf("frequência inválida")
}

func (a *App) saveJob(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	f := r.PostFormValue
	atoi := func(s string) int { n, _ := strconv.Atoi(s); return n }
	j := Job{ID: id, Name: strings.TrimSpace(f("name")), Kind: f("kind"), Src: strings.TrimSpace(f("src")),
		Dst: strings.TrimSpace(f("dst")), Excludes: f("excludes"), Trash: strings.TrimSpace(f("trash")),
		MinAge: strings.TrimSpace(f("min_age")), Mount: strings.TrimSpace(f("mount")),
		Transfers: atoi(f("transfers")), Checkers: atoi(f("checkers"))}

	back := func(msg string) {
		to := "/jobs/new"
		if id != 0 {
			to = fmt.Sprintf("/jobs/%d", id)
		}
		http.Redirect(w, r, to+"?err="+urlEscape(msg), http.StatusSeeOther)
	}
	switch {
	case j.Name == "":
		back("Nome é obrigatório.")
		return
	case j.Kind != "host" && j.Dst == "":
		back("Destino é obrigatório.")
		return
	case j.Kind != "sync" && j.Kind != "copy" && j.Kind != "clean" && j.Kind != "host":
		back("Tipo inválido.")
		return
	case (j.Kind == "sync" || j.Kind == "copy") && j.Src == "":
		back("Origem é obrigatória.")
		return
	case j.Kind == "clean" && j.MinAge == "":
		back("Idade mínima é obrigatória para limpar a lixeira.")
		return
	}
	var err error
	if j.Schedule, err = buildSchedule(f("freq"), f("dow"), f("dom"), f("time"), f("schedule")); err != nil {
		back("Agenda inválida: " + err.Error() + ".")
		return
	}

	if id == 0 {
		res, err := a.db.Exec(`INSERT INTO jobs(name,kind,src,dst,excludes,trash,min_age,mount,schedule,transfers,checkers)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`, j.Name, j.Kind, j.Src, j.Dst, j.Excludes, j.Trash, j.MinAge, j.Mount, j.Schedule, j.Transfers, j.Checkers)
		if err != nil {
			back("Não foi possível salvar: " + err.Error())
			return
		}
		id, _ = res.LastInsertId()
	} else if _, err := a.db.Exec(`UPDATE jobs SET name=?,kind=?,src=?,dst=?,excludes=?,trash=?,min_age=?,mount=?,schedule=?,transfers=?,checkers=? WHERE id=?`,
		j.Name, j.Kind, j.Src, j.Dst, j.Excludes, j.Trash, j.MinAge, j.Mount, j.Schedule, j.Transfers, j.Checkers, id); err != nil {
		back("Não foi possível salvar: " + err.Error())
		return
	}
	saved, err := a.job(id)
	if err != nil {
		fail(w, err)
		return
	}
	a.schedule(saved)
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d", id), http.StatusSeeOther)
}

func (a *App) runJob(w http.ResponseWriter, r *http.Request) {
	j, err := a.job(pathID(r))
	if err != nil || j.Kind == "host" {
		http.NotFound(w, r)
		return
	}
	runID, err := a.Start(j, strings.HasSuffix(r.URL.Path, "/dry"))
	if err != nil {
		http.Redirect(w, r, fmt.Sprintf("/jobs/%d?err=%s", j.ID, urlEscape(err.Error())), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/runs/%d", runID), http.StatusSeeOther)
}

func (a *App) pauseJob(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if _, err := a.db.Exec(`UPDATE jobs SET paused = NOT paused WHERE id=?`, id); err != nil {
		fail(w, err)
		return
	}
	if j, err := a.job(id); err == nil {
		a.schedule(j)
	}
	http.Redirect(w, r, fmt.Sprintf("/jobs/%d", id), http.StatusSeeOther)
}

func (a *App) runPage(w http.ResponseWriter, r *http.Request) {
	rs, err := a.runs(`WHERE r.id=?`, pathID(r))
	if err != nil {
		fail(w, err)
		return
	}
	if len(rs) == 0 {
		http.NotFound(w, r)
		return
	}
	a.mu.Lock()
	live := a.live[rs[0].ID]
	a.mu.Unlock()
	if rs[0].Status != "running" {
		live = nil // terminou: mostra o resultado gravado
	}
	data := map[string]any{"Run": rs[0], "Live": live}
	if strings.HasSuffix(r.URL.Path, "/live") {
		render(w, "run", "live", data)
		return
	}
	render(w, "run", "layout", data)
}

func (a *App) stopRun(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	var rcJob int64
	a.db.QueryRow(`SELECT rc_job FROM runs WHERE id=? AND status='running'`, id).Scan(&rcJob)
	if rcJob != 0 {
		if err := a.rc.Call("job/stop", map[string]any{"jobid": rcJob}, nil); err != nil {
			log.Printf("parar run %d: %v", id, err)
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/runs/%d", id), http.StatusSeeOther)
}

func urlEscape(s string) string { return template.URLQueryEscaper(s) }
