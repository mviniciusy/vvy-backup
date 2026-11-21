package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

type Job struct {
	ID                  int64
	Name, Kind          string // kind: sync | copy | clean
	Src, Dst, Excludes  string
	Trash, MinAge       string
	Mount, Schedule     string
	Transfers, Checkers int
	Paused              bool
}

type step struct {
	path   string
	params map[string]any
}

// steps monta as chamadas à API do rclone para um job.
func steps(j Job, group string, dry bool, now time.Time) []step {
	cfg := map[string]any{"DryRun": dry}
	if j.Transfers > 0 {
		cfg["Transfers"] = j.Transfers
	}
	if j.Checkers > 0 {
		cfg["Checkers"] = j.Checkers
	}
	filter := map[string]any{}
	if ex := lines(j.Excludes); len(ex) > 0 {
		filter["ExcludeRule"] = ex
	}
	base := func() map[string]any {
		return map[string]any{"_async": true, "_group": group, "_config": cfg, "_filter": filter}
	}

	if j.Kind == "clean" {
		filter["MinAge"] = j.MinAge
		del := base()
		del["fs"] = j.Dst
		rm := base()
		rm["fs"], rm["remote"], rm["leaveRoot"] = j.Dst, "", true
		rm["_filter"] = map[string]any{}
		return []step{{"operations/delete", del}, {"operations/rmdirs", rm}}
	}

	p := base()
	p["srcFs"], p["dstFs"] = j.Src, j.Dst
	if j.Kind == "sync" && j.Trash != "" {
		cfg["BackupDir"] = strings.TrimRight(j.Trash, "/") + "/" + now.Format("2006-01-02")
	}
	return []step{{j.Kind + "/" + j.Kind, p}}
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// mounted diz se path aparece como ponto de montagem no conteúdo de /proc/mounts.
func mounted(procMounts, path string) bool {
	path = strings.TrimRight(path, "/")
	for _, l := range strings.Split(procMounts, "\n") {
		f := strings.Fields(l)
		if len(f) > 1 && strings.ReplaceAll(f[1], `\040`, " ") == path {
			return true
		}
	}
	return false
}

var errBusy = errors.New("Outro job está rodando. Tente quando ele terminar.")

func (a *App) check(j Job) error {
	if j.Mount != "" {
		pm, err := os.ReadFile("/proc/mounts")
		if err != nil || !mounted(string(pm), j.Mount) {
			return fmt.Errorf("%s não está montado. Verifique o HD no Proxmox.", j.Mount)
		}
	}
	if j.Kind != "clean" && !strings.Contains(j.Src, ":") {
		if _, err := os.Stat(j.Src); err != nil {
			return fmt.Errorf("Origem %s não existe.", j.Src)
		}
	}
	return nil
}

// Start cria a execução e roda em segundo plano.
// ponytail: um job por vez no app todo (igual aos scripts); fila se um dia precisar de paralelo.
func (a *App) Start(j Job, dry bool) (int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running != 0 {
		return 0, errBusy
	}
	now := time.Now()
	if err := a.check(j); err != nil {
		a.db.Exec(`INSERT INTO runs(job_id,started,finished,status,dry,msg) VALUES(?,?,?,?,?,?)`,
			j.ID, now.Unix(), now.Unix(), "error", dry, err.Error())
		return 0, err
	}
	res, err := a.db.Exec(`INSERT INTO runs(job_id,started,status,dry) VALUES(?,?,?,?)`, j.ID, now.Unix(), "running", dry)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	a.running = id
	go a.execute(id, steps(j, fmt.Sprintf("run/%d", id), dry, now))
	return id, nil
}

func (a *App) execute(runID int64, ss []step) {
	group := fmt.Sprintf("run/%d", runID)
	var msg string
	for _, s := range ss {
		var r struct {
			JobID int64 `json:"jobid"`
		}
		if err := a.rc.Call(s.path, s.params, &r); err != nil {
			msg = err.Error()
			break
		}
		a.db.Exec(`UPDATE runs SET rc_job=? WHERE id=?`, r.JobID, runID)
		if msg = a.wait(runID, r.JobID, group); msg != "" {
			break
		}
	}
	a.finish(runID, group, msg)
}

// wait acompanha um job do rclone até terminar. Retorna a mensagem de erro, se houver.
func (a *App) wait(runID, rcJob int64, group string) string {
	for {
		var st JobStatus
		if err := a.rc.Call("job/status", map[string]any{"jobid": rcJob}, &st); err != nil {
			return err.Error()
		}
		var s Stats
		if a.rc.Call("core/stats", map[string]any{"group": group}, &s) == nil {
			a.mu.Lock()
			a.live[runID] = &s
			a.mu.Unlock()
		}
		if st.Finished {
			if !st.Success {
				if strings.Contains(st.Error, "context canceled") {
					return "Parado manualmente."
				}
				return st.Error
			}
			return ""
		}
		time.Sleep(2 * time.Second)
	}
}

func (a *App) finish(runID int64, group, msg string) {
	var s Stats
	a.rc.Call("core/stats", map[string]any{"group": group}, &s)
	status := "ok"
	if msg != "" || s.Errors > 0 {
		status = "error"
		if msg == "" {
			msg = s.LastError
		}
	}
	_, err := a.db.Exec(`UPDATE runs SET finished=?,status=?,bytes=?,files=?,deletes=?,errors=?,msg=? WHERE id=?`,
		time.Now().Unix(), status, s.Bytes, s.Transfers, s.Deletes, s.Errors, msg, runID)
	if err != nil {
		log.Printf("run %d: %v", runID, err)
	}
	a.rc.Call("core/stats-delete", map[string]any{"group": group}, nil)
	a.mu.Lock()
	delete(a.live, runID)
	if a.running == runID {
		a.running = 0
	}
	a.mu.Unlock()
}

// resume retoma execuções que estavam rodando quando o app reiniciou.
func (a *App) resume() {
	rows, err := a.db.Query(`SELECT id, rc_job FROM runs WHERE status='running'`)
	if err != nil {
		log.Fatal(err)
	}
	type r struct{ id, rc int64 }
	var rs []r
	for rows.Next() {
		var x r
		rows.Scan(&x.id, &x.rc)
		rs = append(rs, x)
	}
	rows.Close()
	for _, x := range rs {
		if x.rc == 0 {
			a.finish(x.id, "", "Interrompido antes de começar.")
			continue
		}
		a.running = x.id
		go func(x r) {
			group := fmt.Sprintf("run/%d", x.id)
			msg := a.wait(x.id, x.rc, group)
			if strings.Contains(msg, "job not found") {
				msg = "Interrompido: o rclone reiniciou durante a execução."
			}
			a.finish(x.id, group, msg)
		}(x)
	}
}
