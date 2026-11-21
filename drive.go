package main

import (
	"fmt"
	"html/template"
	"log"
	"sort"
	"strings"
	"time"
)

type part struct {
	Name  string
	Bytes int64
	Color string
	Pct   float64
}

// Drive guarda o uso do Google Drive por pasta de backup (calculado em segundo plano).
type Drive struct {
	At          time.Time
	Busy        bool
	Err         string
	Total, Used int64
	Trash       int64
	Parts       []part
}

var palette = []string{"#3b82f6", "#22c55e", "#ec4899", "#a855f7", "#06b6d4", "#eab308", "#f97316", "#ef4444", "#14b8a6"}

func (a *App) drive() Drive {
	a.dmu.Lock()
	defer a.dmu.Unlock()
	return a.drv
}

// refreshDrive mede cada pasta de a.base. ponytail: rclone size por pasta, lento em pastas enormes; cache de 6 h.
func (a *App) refreshDrive() {
	a.dmu.Lock()
	if a.drv.Busy {
		a.dmu.Unlock()
		return
	}
	a.drv.Busy = true
	a.dmu.Unlock()

	d := Drive{At: time.Now()}
	defer func() {
		a.dmu.Lock()
		if d.Err != "" && len(a.drv.Parts) > 0 { // mantém o último resultado bom
			a.drv.Busy, a.drv.Err = false, d.Err
		} else {
			a.drv = d
		}
		a.dmu.Unlock()
		if d.Err != "" {
			log.Printf("drive: %s", d.Err)
		}
	}()

	root := a.base[:strings.Index(a.base, ":")+1]
	var ab struct{ Total, Used int64 }
	if err := a.rc.Call("operations/about", map[string]any{"fs": root}, &ab); err != nil {
		d.Err = err.Error()
		return
	}
	d.Total, d.Used = ab.Total, ab.Used

	var ls struct {
		List []struct{ Name string }
	}
	if err := a.rc.Call("operations/list", map[string]any{"fs": a.base, "remote": "", "opt": map[string]any{"dirsOnly": true}}, &ls); err != nil {
		d.Err = err.Error()
		return
	}
	var sum int64
	for _, e := range ls.List {
		var sz struct{ Bytes int64 }
		if err := a.rc.Call("operations/size", map[string]any{"fs": a.base + "/" + e.Name}, &sz); err != nil {
			d.Err = err.Error()
			return
		}
		if e.Name == "lixeira" {
			d.Trash = sz.Bytes
		}
		d.Parts = append(d.Parts, part{Name: e.Name, Bytes: sz.Bytes})
		sum += sz.Bytes
	}
	sort.Slice(d.Parts, func(i, j int) bool { return d.Parts[i].Bytes > d.Parts[j].Bytes })
	if other := d.Used - sum; other > 0 {
		d.Parts = append(d.Parts, part{Name: "Fora do backup", Bytes: other})
		sum += other
	}
	for i := range d.Parts {
		d.Parts[i].Color = palette[i%len(palette)]
		if d.Parts[i].Name == "Fora do backup" {
			d.Parts[i].Color = "#71717a"
		}
		if sum > 0 {
			d.Parts[i].Pct = float64(d.Parts[i].Bytes) * 100 / float64(sum)
		}
	}
}

func segStyle(color string, pct float64) template.CSS {
	return template.CSS(fmt.Sprintf("--c:%s;flex-basis:%.2f%%", color, pct))
}

func dotStyle(color string) template.CSS { return template.CSS("--c:" + color) }
