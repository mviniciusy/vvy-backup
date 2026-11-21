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
	"bars":  bars,
	"rate":  func(v float64) string { return humanBytes(int64(v)) + "/s" },
	"list":  func(s ...string) []string { return s },
	"human": humanSchedule,
	"sub":   func(a, b int64) int64 { return a - b },
}

func humanBytes(n int64) string {
	if n < 0 {
		return "-" + humanBytes(-n)
	}
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

// bars desenha colunas em grade de pixels (CSS), rotulando só o maior valor.
func bars(bs []bar) template.HTML {
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
	var sb strings.Builder
	sb.WriteString(`<div class="cols" role="img">`)
	for _, b := range bs {
		fmt.Fprintf(&sb, `<span style="height:%.1f%%" title="%s: %s"></span>`, b.V/max*100,
			template.HTMLEscapeString(b.Label), template.HTMLEscapeString(b.Text))
	}
	fmt.Fprintf(&sb, `</div><div class="cols-foot"><span>%s</span><span>máx. %s em %s</span><span>%s</span></div>`,
		template.HTMLEscapeString(bs[0].Label), template.HTMLEscapeString(bs[maxI].Text),
		template.HTMLEscapeString(bs[maxI].Label), template.HTMLEscapeString(bs[len(bs)-1].Label))
	return template.HTML(sb.String())
}

// sameOrigin barra POST vindo de outros sites (CSRF): o navegador reenvia o basic auth sozinho.
// Pedidos sem Origin nem Sec-Fetch-Site (curl, bm-run) passam.
func sameOrigin(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return true
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin" || site == "none"
	}
	if o := r.Header.Get("Origin"); o != "" {
		return o == "http://"+r.Host || o == "https://"+r.Host
	}
	return true
}

func basicAuth(user, pass string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			http.Error(w, "Pedido de outro site recusado.", http.StatusForbidden)
			return
		}
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
	m.HandleFunc("GET /api/drive", a.driveJSON)
	m.HandleFunc("GET /api/disks", a.disksJSON)
	m.HandleFunc("GET /api/running", a.runningJSON)
	m.HandleFunc("POST /drive/refresh", func(w http.ResponseWriter, r *http.Request) {
		if f := r.URL.Query().Get("folder"); f != "" {
			a.refreshFolder(f) // síncrono: a página espera o novo tamanho
		} else {
			go func() { a.refreshDrive(); a.refreshDisks() }()
		}
		w.WriteHeader(http.StatusNoContent)
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
	if err1 != nil || err2 != nil || err3 != nil || code < 0 || code > 255 || started <= 0 || finished < started {
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
	http.Error(w, "Erro interno. Veja o log do serviço (journalctl -u vvy-backup).", http.StatusInternalServerError)
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

	running := a.currentRun()
	heat, heatDays, err := a.heatmap(jobs, 30)
	if err != nil {
		fail(w, err)
		return
	}
	a.dmu.Lock()
	disks := a.disks
	a.dmu.Unlock()
	var trashJob *Job
	for i := range jobs {
		if jobs[i].Kind == "clean" {
			trashJob = &jobs[i]
		}
	}
	plan := upcomingDays(jobs, 7)
	next := ""
	for _, d := range plan {
		if len(d.Items) > 0 {
			it := d.Items[0]
			next = it.At.Format("15:04") + "|" + it.Job.Name + ", " + strings.ToLower(d.Label)
			break
		}
	}
	drv := a.drive()
	ss, labels := a.volumeSeries(jobs, 30)
	now := time.Now()
	render(w, "painel", "layout", map[string]any{"Rows": rows, "Running": running, "Nav": a.nav(0), "Crumb": "Painel",
		"Month": months[now.Month()-1], "Year": now.Year(), "KPIs": a.kpis(a.lastDrive(), next),
		"Lines": lineChart(ss, labels), "Series": ss,
		"Cfg": map[string]any{"drive": drv, "disks": disks, "running": running, "base": a.base,
			"trashJob": trashID(trashJob), "trashAge": trashAge(trashJob)},
		"Heat": heat, "HeatDays": heatDays, "Plan": plan, "Health": a.health(jobs), "Growth": a.growth()})
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
		"Nav": a.nav(j.ID), "Crumb": "Jobs",
		"S": parseSchedule(j.Schedule),
	})
}

var weekdays = []string{"domingo", "segunda", "terça", "quarta", "quinta", "sexta", "sábado"}

// humanSchedule escreve a agenda em português ("Toda segunda às 03:00").
func humanSchedule(s string) string {
	p := parseSchedule(s)
	switch p.Freq {
	case "none":
		return "Sem agenda"
	case "daily":
		return "Todo dia às " + p.Time
	case "weekly":
		d, _ := strconv.Atoi(p.Dow)
		if d < 0 || d >= len(weekdays) {
			return "Cron " + s
		}
		if d == 0 || d == 6 {
			return "Todo " + weekdays[d] + " às " + p.Time
		}
		return "Toda " + weekdays[d] + " às " + p.Time
	case "monthly":
		return "Todo dia " + p.Dom + " às " + p.Time
	}
	return "Cron " + s
}

func trashID(j *Job) int64 {
	if j == nil {
		return 0
	}
	return j.ID
}

func trashAge(j *Job) string {
	if j == nil {
		return ""
	}
	if n, err := trashDays(j.MinAge); err == nil {
		return fmt.Sprintf("%d dias", n)
	}
	return j.MinAge
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
		if ok1 && ok2 && m >= 0 && m <= 59 && h >= 0 && h <= 23 {
			t := fmt.Sprintf("%02d:%02d", h, m)
			dow, dowOK := num(f[4])
			dom, domOK := num(f[2])
			dowOK = dowOK && dow >= 0 && dow <= 6
			domOK = domOK && dom >= 1 && dom <= 28
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
		if len(strings.Fields(raw)) != 5 { // recusa @every e afins, que podem disparar a cada segundo
			return "", fmt.Errorf("use 5 campos: minuto hora dia mês dia-da-semana")
		}
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
	}
	if j.Kind == "clean" {
		if _, err := trashDays(j.MinAge); err != nil {
			back("Idade mínima inválida: " + err.Error() + ".")
			return
		}
		j.Excludes = "" // exclusões não se aplicam à limpeza
	}
	if j.Transfers < 1 {
		j.Transfers = 4
	}
	if j.Checkers < 1 {
		j.Checkers = 8
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
	data["Nav"], data["Crumb"] = a.nav(rs[0].JobID), "Execução"
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
