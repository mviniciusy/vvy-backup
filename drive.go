package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

type part struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	Color string `json:"color"`
	Icon  string `json:"icon"`
	Desc  string `json:"desc"`
}

// Drive é o uso da cota do Google por pasta de backup, medido em segundo plano.
type Drive struct {
	At      time.Time `json:"at"`
	Busy    bool      `json:"busy"`
	Current string    `json:"current"` // pasta sendo medida agora
	Err     string    `json:"err"`
	Total   int64     `json:"total"`
	Used    int64     `json:"used"`
	Trash   int64     `json:"trash"`
	Parts   []part    `json:"parts"`
}

var palette = []string{"#3b82f6", "#22c55e", "#ec4899", "#a855f7", "#06b6d4", "#eab308", "#f97316", "#14b8a6"}

func (a *App) drive() Drive {
	a.dmu.Lock()
	defer a.dmu.Unlock()
	if a.drv.Busy && a.scan != nil {
		return *a.scan
	}
	return a.drv
}

// lastDrive é a última medição completa (ignora a que está em andamento).
func (a *App) lastDrive() Drive {
	a.dmu.Lock()
	defer a.dmu.Unlock()
	return a.drv
}

func (a *App) driveJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(a.drive())
}

// refreshDrive mede cada pasta de a.base e publica o progresso em a.scan.
// ponytail: rclone size por pasta, lento em pastas enormes; roda a cada 6 h ou no botão.
func (a *App) refreshDrive() {
	a.dmu.Lock()
	if a.drv.Busy {
		a.dmu.Unlock()
		return
	}
	a.drv.Busy = true
	a.scan = &Drive{Busy: true, Total: a.drv.Total}
	a.dmu.Unlock()

	d := Drive{At: time.Now()}
	publish := func() {
		a.dmu.Lock()
		cp := d
		cp.Busy = true
		cp.Parts = append([]part(nil), d.Parts...)
		a.scan = &cp
		a.dmu.Unlock()
	}
	defer func() {
		a.dmu.Lock()
		if d.Err != "" && len(a.drv.Parts) > 0 { // mantém o último resultado bom
			a.drv.Busy, a.drv.Err = false, d.Err
		} else {
			d.Current = ""
			a.drv = d
		}
		a.scan = nil
		a.dmu.Unlock()
		if d.Err == "" {
			a.saveHist(d)
		}
		if d.Err != "" {
			log.Printf("drive: %s", d.Err)
		}
	}()

	root := a.base[:strings.Index(a.base, ":")+1]
	// used = arquivos do Drive; other = Gmail, Fotos etc.; free = o que a cota ainda permite.
	var ab struct{ Total, Used, Trashed, Other, Free int64 }
	if err := a.rc.Call("operations/about", map[string]any{"fs": root}, &ab); err != nil {
		d.Err = err.Error()
		return
	}
	d.Total = ab.Total
	publish()

	var ls struct {
		List []struct{ Name string }
	}
	if err := a.rc.Call("operations/list", map[string]any{"fs": a.base, "remote": "", "opt": map[string]any{"dirsOnly": true}}, &ls); err != nil {
		d.Err = err.Error()
		return
	}
	descs := a.folderDescs()
	var sum int64
	for _, e := range ls.List {
		d.Current = a.base + "/" + e.Name
		publish()
		var sz struct{ Bytes int64 }
		if err := a.rc.Call("operations/size", map[string]any{"fs": a.base + "/" + e.Name}, &sz); err != nil {
			d.Err = err.Error()
			return
		}
		p := part{Name: e.Name, Bytes: sz.Bytes, Icon: iconFor(e.Name), Desc: descs[e.Name]}
		if p.Desc == "" {
			p.Desc = map[string]string{
				"layers": "Snapshots enviados pelo host.",
				"gear":   "Configuração enviada pelo host.",
			}[p.Icon]
		}
		if p.Desc == "" {
			p.Desc = "Enviada pelo host."
		}
		if strings.EqualFold(e.Name, "lixeira") {
			d.Trash, p.Color = sz.Bytes, "#ef4444"
		}
		d.Parts = append(d.Parts, p)
		d.Used += sz.Bytes
		sum += sz.Bytes
	}
	d.Current = ""
	sort.SliceStable(d.Parts, func(i, j int) bool { return d.Parts[i].Bytes > d.Parts[j].Bytes })
	for i := range d.Parts { // cor pela posição final, para não trocar entre medições
		if d.Parts[i].Color == "" {
			d.Parts[i].Color = palette[i%len(palette)]
		}
	}
	for _, p := range []part{
		{Name: "Resto do Drive", Bytes: ab.Used - sum, Color: "#b4b4bb", Icon: "cloud", Desc: "Seus arquivos fora do backup."},
		{Name: "Gmail e Fotos", Bytes: ab.Other, Color: "#8e8e96", Icon: "mail", Desc: "Usam a mesma cota do Drive."},
		{Name: "Lixeira do Google", Bytes: ab.Trashed, Color: "#6b6b73", Icon: "trash", Desc: "O Google apaga em 30 dias."},
	} {
		if p.Bytes > 0 {
			d.Parts = append(d.Parts, p)
		}
	}
	d.Used = max(ab.Total-ab.Free, 0)
}

func iconFor(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "lixeira"):
		return "trash"
	case strings.HasPrefix(n, "hd"):
		return "hdd"
	case strings.HasPrefix(n, "ssd"):
		return "layers"
	case strings.Contains(n, "config"):
		return "gear"
	}
	return "folder"
}

// folderDescs liga cada pasta do Drive ao job que a envia, para o texto do hover.
func (a *App) folderDescs() map[string]string {
	out := map[string]string{}
	jobs, err := a.jobs()
	if err != nil {
		return out
	}
	for _, j := range jobs {
		if !strings.HasPrefix(j.Dst, a.base+"/") {
			continue
		}
		folder := strings.SplitN(strings.TrimPrefix(j.Dst, a.base+"/"), "/", 2)[0]
		desc := "Do job " + j.Name
		if rs, err := a.runs(`WHERE r.job_id=? AND r.dry=0 AND r.status='ok' ORDER BY r.started DESC LIMIT 1`, j.ID); err == nil && len(rs) > 0 {
			desc += ", enviado em " + time.Unix(rs[0].Started, 0).Format("02/01 15:04")
		}
		if j.Kind == "clean" {
			desc = "Removidos dos espelhos. Saem após " + trashAge(&j)
		}
		out[folder] = desc + "."
	}
	if _, ok := out["lixeira"]; !ok {
		out["lixeira"] = "Removidos dos espelhos."
	}
	return out
}

// refreshFolder mede de novo uma pasta só (ex.: depois de limpar a lixeira).
func (a *App) refreshFolder(name string) {
	var sz struct{ Bytes int64 }
	if err := a.rc.Call("operations/size", map[string]any{"fs": a.base + "/" + name}, &sz); err != nil {
		log.Printf("drive %s: %v", name, err)
		return
	}
	a.dmu.Lock()
	defer a.dmu.Unlock()
	if a.drv.Busy { // a medição completa em andamento já vai trazer o valor novo
		return
	}
	for i, p := range a.drv.Parts {
		if p.Name == name {
			a.drv.Used += sz.Bytes - p.Bytes
			a.drv.Parts[i].Bytes = sz.Bytes
			if strings.EqualFold(name, "lixeira") {
				a.drv.Trash = sz.Bytes
			}
		}
	}
}
