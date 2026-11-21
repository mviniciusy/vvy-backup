package main

import (
	"database/sql"
	"flag"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	_ "modernc.org/sqlite"
)

type App struct {
	db   *sql.DB
	rc   *RC
	cron *cron.Cron

	mu      sync.Mutex
	running int64            // id da execução em andamento (0 = nenhuma)
	live    map[int64]*Stats // estatísticas ao vivo por execução
	entries map[int64]cron.EntryID

	base string // pasta base dos backups no Drive
	dmu  sync.Mutex
	drv  Drive
}

const schema = `
CREATE TABLE IF NOT EXISTS jobs(
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL DEFAULT 'sync',
  src TEXT NOT NULL DEFAULT '',
  dst TEXT NOT NULL,
  excludes TEXT NOT NULL DEFAULT '',
  trash TEXT NOT NULL DEFAULT '',
  min_age TEXT NOT NULL DEFAULT '',
  mount TEXT NOT NULL DEFAULT '',
  schedule TEXT NOT NULL DEFAULT '',
  transfers INTEGER NOT NULL DEFAULT 4,
  checkers INTEGER NOT NULL DEFAULT 8,
  paused INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS runs(
  id INTEGER PRIMARY KEY,
  job_id INTEGER NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  started INTEGER NOT NULL,
  finished INTEGER,
  status TEXT NOT NULL,
  dry INTEGER NOT NULL DEFAULT 0,
  bytes INTEGER NOT NULL DEFAULT 0,
  files INTEGER NOT NULL DEFAULT 0,
  deletes INTEGER NOT NULL DEFAULT 0,
  errors INTEGER NOT NULL DEFAULT 0,
  msg TEXT NOT NULL DEFAULT '',
  rc_job INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS runs_job ON runs(job_id, started DESC);`

func main() {
	addr := flag.String("addr", ":8080", "endereço HTTP")
	dbPath := flag.String("db", "/var/lib/vvy-backup/vvy-backup.db", "arquivo SQLite")
	rcURL := flag.String("rc", "http://127.0.0.1:5572", "API do rclone rcd")
	base := flag.String("drive-base", "gdrive:backup", "pasta dos backups no Drive (o painel mede cada subpasta)")
	flag.Parse()

	user, pass := os.Getenv("BM_USER"), os.Getenv("BM_PASS")
	if user == "" || pass == "" {
		log.Fatal("defina BM_USER e BM_PASS")
	}

	db, err := sql.Open("sqlite", "file:"+*dbPath+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		log.Fatal(err)
	}

	a := &App{
		db:      db,
		rc:      &RC{URL: *rcURL, User: os.Getenv("RC_USER"), Pass: os.Getenv("RC_PASS")},
		cron:    cron.New(),
		live:    map[int64]*Stats{},
		entries: map[int64]cron.EntryID{},
		base:    *base,
	}
	go func() {
		for {
			a.refreshDrive()
			time.Sleep(6 * time.Hour)
		}
	}()
	a.resume()
	jobs, err := a.jobs()
	if err != nil {
		log.Fatal(err)
	}
	for _, j := range jobs {
		a.schedule(j)
	}
	a.cron.Start()

	log.Printf("ouvindo em %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, basicAuth(user, pass, a.routes())))
}

// schedule (re)registra o job no cron interno.
func (a *App) schedule(j Job) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id, ok := a.entries[j.ID]; ok {
		a.cron.Remove(id)
		delete(a.entries, j.ID)
	}
	if j.Paused || j.Schedule == "" || j.Kind == "host" { // jobs do host rodam pelo cron do vvy
		return nil
	}
	id, err := a.cron.AddFunc(j.Schedule, func() {
		cur, err := a.job(j.ID)
		if err != nil || cur.Paused {
			return
		}
		if _, err := a.Start(cur, false); err != nil {
			log.Printf("job %s: %v", cur.Name, err)
		}
	})
	if err != nil {
		return err
	}
	a.entries[j.ID] = id
	return nil
}

const jobCols = `id,name,kind,src,dst,excludes,trash,min_age,mount,schedule,transfers,checkers,paused`

func scanJob(s interface{ Scan(...any) error }) (Job, error) {
	var j Job
	err := s.Scan(&j.ID, &j.Name, &j.Kind, &j.Src, &j.Dst, &j.Excludes, &j.Trash, &j.MinAge,
		&j.Mount, &j.Schedule, &j.Transfers, &j.Checkers, &j.Paused)
	return j, err
}

func (a *App) job(id int64) (Job, error) {
	return scanJob(a.db.QueryRow(`SELECT `+jobCols+` FROM jobs WHERE id=?`, id))
}

func (a *App) jobs() ([]Job, error) {
	rows, err := a.db.Query(`SELECT ` + jobCols + ` FROM jobs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
