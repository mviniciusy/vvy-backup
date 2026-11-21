package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

type Job struct {
	ID                  int64
	Name, Kind          string // kind: sync | copy | clean | host
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

// syncStep monta a chamada à API do rclone para um job de espelho ou cópia.
func syncStep(j Job, group string, dry bool, now time.Time) step {
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
	if j.Kind == "sync" && j.Trash != "" {
		cfg["BackupDir"] = strings.TrimRight(j.Trash, "/") + "/" + now.Format("2006-01-02")
	}
	return step{j.Kind + "/" + j.Kind, map[string]any{
		"_async": true, "_group": group, "_config": cfg, "_filter": filter, "srcFs": j.Src, "dstFs": j.Dst,
	}}
}

// trashDays lê a idade mínima da lixeira ("14d" ou "14").
func trashDays(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(s), "d"))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("idade mínima deve ser um número de dias, ex. 14d")
	}
	return n, nil
}

// oldTrashDirs escolhe, entre pastas relativas, as datadas (AAAA-MM-DD) anteriores a cutoff.
// A data é a do envio para a lixeira, não a de modificação dos arquivos.
func oldTrashDirs(dirs []string, cutoff time.Time) []string {
	day := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, time.Local)
	var out []string
	for _, d := range dirs {
		t, err := time.ParseInLocation("2006-01-02", path.Base(d), time.Local)
		if err == nil && t.Before(day) {
			out = append(out, d)
		}
	}
	return out
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
	if j.Kind == "clean" {
		_, err := trashDays(j.MinAge) // sem idade mínima, a limpeza apagaria a lixeira inteira
		return err
	}
	if j.Mount != "" {
		pm, err := os.ReadFile("/proc/mounts")
		if err != nil || !mounted(string(pm), j.Mount) {
			return fmt.Errorf("%s não está montado. Verifique o HD no Proxmox.", j.Mount)
		}
	}
	if !strings.Contains(j.Src, ":") {
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
	if a.running != 0 {
		a.mu.Unlock()
		return 0, errBusy
	}
	a.running = -1 // reservado: a checagem de disco roda fora da trava
	a.mu.Unlock()
	release := func() {
		a.mu.Lock()
		if a.running == -1 {
			a.running = 0
		}
		a.mu.Unlock()
	}

	now := time.Now()
	if err := a.check(j); err != nil {
		a.db.Exec(`INSERT INTO runs(job_id,started,finished,status,dry,msg) VALUES(?,?,?,?,?,?)`,
			j.ID, now.Unix(), now.Unix(), "error", dry, err.Error())
		release()
		return 0, err
	}
	res, err := a.db.Exec(`INSERT INTO runs(job_id,started,status,dry) VALUES(?,?,?,?)`, j.ID, now.Unix(), "running", dry)
	if err != nil {
		release()
		return 0, err
	}
	id, _ := res.LastInsertId()
	a.mu.Lock()
	a.running = id
	a.mu.Unlock()
	go a.execute(id, j, dry, now)
	return id, nil
}

// currentRun devolve a execução em andamento (0 quando não há, ou enquanto só está reservada).
func (a *App) currentRun() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return max(a.running, 0)
}

func (a *App) execute(runID int64, j Job, dry bool, now time.Time) {
	group := fmt.Sprintf("run/%d", runID)
	var msg string
	if j.Kind == "clean" {
		msg = a.cleanTrash(runID, j, dry, group)
	} else {
		msg = a.runStep(runID, syncStep(j, group, dry, now), group)
	}
	a.finish(runID, group, msg)
}

func (a *App) runStep(runID int64, s step, group string) string {
	var r struct {
		JobID int64 `json:"jobid"`
	}
	if err := a.rc.Call(s.path, s.params, &r); err != nil {
		return err.Error()
	}
	if _, err := a.db.Exec(`UPDATE runs SET rc_job=? WHERE id=?`, r.JobID, runID); err != nil {
		log.Printf("run %d: %v", runID, err)
	}
	return a.wait(runID, r.JobID, group)
}

// cleanTrash apaga as pastas datadas da lixeira (dst/AAAA-MM-DD ou dst/<job>/AAAA-MM-DD)
// que passaram da idade mínima.
func (a *App) cleanTrash(runID int64, j Job, dry bool, group string) string {
	days, err := trashDays(j.MinAge)
	if err != nil {
		return err.Error()
	}
	list := func(remote string) ([]string, error) {
		var ls struct {
			List []struct{ Path string }
		}
		err := a.rc.Call("operations/list", map[string]any{"fs": j.Dst, "remote": remote, "opt": map[string]any{"dirsOnly": true}}, &ls)
		var out []string
		for _, e := range ls.List {
			out = append(out, e.Path)
		}
		return out, err
	}
	top, err := list("")
	if err != nil {
		return err.Error()
	}
	dirs := top
	for _, d := range top {
		if _, perr := time.Parse("2006-01-02", path.Base(d)); perr != nil { // pasta de um job: olha as datas dentro
			sub, err := list(d)
			if err != nil {
				return err.Error()
			}
			dirs = append(dirs, sub...)
		}
	}
	for _, d := range oldTrashDirs(dirs, time.Now().AddDate(0, 0, -days)) {
		s := step{"operations/purge", map[string]any{"fs": j.Dst, "remote": d, "_async": true, "_group": group,
			"_config": map[string]any{"DryRun": dry}}}
		if msg := a.runStep(runID, s, group); msg != "" {
			return msg
		}
	}
	return ""
}

// wait acompanha um job do rclone até terminar. Retorna a mensagem de erro, se houver.
// Falhas passageiras de rede são repetidas: desistir cedo liberaria outro job com este ainda rodando.
func (a *App) wait(runID, rcJob int64, group string) string {
	fails := 0
	for {
		var st JobStatus
		if err := a.rc.Call("job/status", map[string]any{"jobid": rcJob}, &st); err != nil {
			if strings.Contains(err.Error(), "job not found") || fails >= 10 {
				return err.Error()
			}
			fails++
			time.Sleep(time.Duration(fails) * 3 * time.Second)
			continue
		}
		fails = 0
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
	if group != "" { // sem grupo, core/stats devolveria os números do servidor inteiro
		a.rc.Call("core/stats", map[string]any{"group": group}, &s)
	}
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
	if group != "" {
		a.rc.Call("core/stats-delete", map[string]any{"group": group}, nil)
	}
	a.mu.Lock()
	delete(a.live, runID)
	if a.running == runID {
		a.running = 0
	}
	a.mu.Unlock()
}

// resume retoma a execução que estava rodando quando o app reiniciou. Só uma pode estar ativa;
// eventuais sobras ficam como erro.
func (a *App) resume() {
	rows, err := a.db.Query(`SELECT id, rc_job FROM runs WHERE status='running' ORDER BY id DESC`)
	if err != nil {
		log.Fatal(err)
	}
	type r struct{ id, rc int64 }
	var rs []r
	for rows.Next() {
		var x r
		if err := rows.Scan(&x.id, &x.rc); err == nil {
			rs = append(rs, x)
		}
	}
	rows.Close()
	for i, x := range rs {
		if x.rc == 0 || i > 0 {
			a.finish(x.id, "", "Interrompido: o app reiniciou durante a execução.")
			continue
		}
		a.mu.Lock()
		a.running = x.id
		a.mu.Unlock()
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
