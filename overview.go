package main

import (
	"fmt"
	"html/template"
	"math"
	"strings"
	"time"
)

// ---------- Barra lateral ----------

type navJob struct {
	ID     int64
	Name   string
	Color  string
	Count  int // execuções reais no mês
	Status string
	Active bool
}

// jobColor dá a cada job a mesma cor na barra lateral e nas linhas do gráfico.
func jobColor(i int) string { return palette[i%len(palette)] }

func (a *App) nav(activeJob int64) []navJob {
	jobs, err := a.jobs()
	if err != nil {
		return nil
	}
	month := monthStart(time.Now())
	counts := map[int64]int{}
	if rs, err := a.runs(`WHERE r.dry=0 AND r.started>=?`, month.Unix()); err == nil {
		for _, r := range rs {
			counts[r.JobID]++
		}
	}
	var out []navJob
	for i, j := range jobs {
		n := navJob{ID: j.ID, Name: j.Name, Color: jobColor(i), Count: counts[j.ID], Active: j.ID == activeJob}
		if j.Paused {
			n.Status = "paused"
		} else if rs, err := a.runs(`WHERE r.job_id=? AND r.dry=0 ORDER BY r.started DESC LIMIT 1`, j.ID); err == nil && len(rs) > 0 {
			n.Status = rs[0].Status
		}
		out = append(out, n)
	}
	return out
}

func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.Local)
}

var months = []string{"Janeiro", "Fevereiro", "Março", "Abril", "Maio", "Junho", "Julho", "Agosto",
	"Setembro", "Outubro", "Novembro", "Dezembro"}

// ---------- Números do mês ----------

type kpi struct {
	Label, Value, Delta string
	Good                bool // a variação é boa (verde) ou ruim (vermelha)
	HasDelta            bool
}

func (a *App) kpis(d Drive, next string) []kpi {
	now := time.Now()
	cur, prev := monthStart(now), monthStart(now).AddDate(0, -1, 0)
	type agg struct {
		bytes        int64
		runs, failed int
	}
	sum := func(from, to time.Time) agg {
		var g agg
		rs, err := a.runs(`WHERE r.dry=0 AND r.started>=? AND r.started<?`, from.Unix(), to.Unix())
		if err != nil {
			return g
		}
		for _, r := range rs {
			g.bytes += r.Bytes
			g.runs++
			if r.Status == "error" {
				g.failed++
			}
		}
		return g
	}
	c, p := sum(cur, now.Add(time.Minute)), sum(prev, cur)

	sent := kpi{Label: "Enviado no mês", Value: humanBytes(c.bytes)}
	if p.bytes > 0 {
		pct := float64(c.bytes-p.bytes) / float64(p.bytes) * 100
		sent.Delta, sent.Good, sent.HasDelta = fmt.Sprintf("%+.0f%% vs %s", pct, strings.ToLower(months[prev.Month()-1])), true, true
	}
	runs := kpi{Label: "Execuções no mês", Value: fmt.Sprintf("%d", c.runs)}
	switch {
	case c.failed > 0:
		runs.Delta, runs.Good, runs.HasDelta = fmt.Sprintf("%d com falha", c.failed), false, true
	case c.runs > 0:
		runs.Delta, runs.Good, runs.HasDelta = "nenhuma falha", true, true
	}
	free := kpi{Label: "Livre no Drive", Value: "—"}
	if d.Total > 0 {
		free.Value = humanBytes(d.Total - d.Used)
		free.Delta, free.Good, free.HasDelta = fmt.Sprintf("%.0f%% da cota", float64(d.Total-d.Used)*100/float64(d.Total)), true, true
	}
	nx := kpi{Label: "Próxima execução", Value: "—"}
	if next != "" {
		parts := strings.SplitN(next, "|", 2)
		nx.Value, nx.Delta, nx.Good, nx.HasDelta = parts[0], parts[1], true, true
	}
	return []kpi{sent, runs, free, nx}
}

// ---------- Gráfico de linhas: enviado por job (em cima) e apagados (embaixo) ----------

type series struct {
	Name, Color string
	Up          []float64 // bytes enviados por dia
	Down        []float64 // arquivos apagados por dia
}

func (a *App) volumeSeries(jobs []Job, days int) ([]series, []string) {
	start := time.Now().AddDate(0, 0, -(days - 1))
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
	var labels []string
	for d := 0; d < days; d++ {
		labels = append(labels, start.AddDate(0, 0, d).Format("02/01"))
	}
	idx := map[int64]int{}
	var out []series
	for i, j := range jobs {
		if j.Kind == "host" {
			continue
		}
		idx[j.ID] = len(out)
		out = append(out, series{Name: j.Name, Color: jobColor(i), Up: make([]float64, days), Down: make([]float64, days)})
	}
	rs, err := a.runs(`WHERE r.dry=0 AND r.started>=?`, start.Unix())
	if err != nil {
		return out, labels
	}
	for _, r := range rs {
		k, ok := idx[r.JobID]
		d := dayIndex(start, time.Unix(r.Started, 0))
		if !ok || d < 0 || d >= days {
			continue
		}
		out[k].Up[d] += float64(r.Bytes)
		out[k].Down[d] += float64(r.Deletes)
	}
	return out, labels
}

// smoothPath liga os pontos com curvas (Catmull-Rom convertido em Bézier).
func smoothPath(xs, ys []float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "M%.1f %.1f", xs[0], ys[0])
	for i := 0; i < len(xs)-1; i++ {
		p0, p3 := max(i-1, 0), min(i+2, len(xs)-1)
		c1x, c1y := xs[i]+(xs[i+1]-xs[p0])/6, ys[i]+(ys[i+1]-ys[p0])/6
		c2x, c2y := xs[i+1]-(xs[p3]-xs[i])/6, ys[i+1]-(ys[p3]-ys[i])/6
		fmt.Fprintf(&b, " C%.1f %.1f %.1f %.1f %.1f %.1f", c1x, c1y, c2x, c2y, xs[i+1], ys[i+1])
	}
	return b.String()
}

func lineChart(ss []series, labels []string) template.HTML {
	var maxUp, maxDown float64
	for _, s := range ss {
		for d := range s.Up {
			maxUp, maxDown = math.Max(maxUp, s.Up[d]), math.Max(maxDown, s.Down[d])
		}
	}
	if maxUp == 0 && maxDown == 0 || len(labels) < 2 {
		return ""
	}
	const w, left, mid, hUp, hDown = 1000.0, 64.0, 190.0, 170.0, 90.0
	plotW := w - left - 8
	x := func(i int) float64 { return left + plotW*float64(i)/float64(len(labels)-1) }
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="lines" viewBox="0 0 %.0f %.0f" role="img" aria-label="Volume enviado e arquivos apagados por dia">`, w, mid+hDown+28)
	b.WriteString(`<defs><filter id="glow" x="-5%" y="-30%" width="110%" height="160%"><feGaussianBlur stdDeviation="4" result="b"/>` +
		`<feMerge><feMergeNode in="b"/><feMergeNode in="SourceGraphic"/></feMerge></filter></defs>`)
	// grade e eixo
	for k := 0; k <= 4; k++ {
		y := mid - hUp*float64(k)/4
		fmt.Fprintf(&b, `<line x1="%.0f" x2="%.0f" y1="%.1f" y2="%.1f" class="grid"/>`, left, w-8, y, y)
		fmt.Fprintf(&b, `<text x="%.0f" y="%.1f" class="tick" text-anchor="end">%s</text>`, left-10, y+4, humanBytes(int64(maxUp*float64(k)/4)))
	}
	for k := 1; k <= 2; k++ {
		y := mid + hDown*float64(k)/2
		fmt.Fprintf(&b, `<line x1="%.0f" x2="%.0f" y1="%.1f" y2="%.1f" class="grid"/>`, left, w-8, y, y)
		fmt.Fprintf(&b, `<text x="%.0f" y="%.1f" class="tick" text-anchor="end">%.0f</text>`, left-10, y+4, maxDown*float64(k)/2)
	}
	for i := 0; i < len(labels); i += 7 {
		fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" class="tick" text-anchor="middle">%s</text>`, x(i), mid+hDown+22, labels[i])
	}
	// apagados (cinza, para baixo) e enviados (coloridos, para cima)
	for _, s := range ss {
		if maxDown == 0 {
			break
		}
		xs, ys := make([]float64, len(s.Down)), make([]float64, len(s.Down))
		for d, v := range s.Down {
			xs[d], ys[d] = x(d), mid+hDown*v/maxDown
		}
		fmt.Fprintf(&b, `<path d="%s" class="down"/>`, smoothPath(xs, ys))
	}
	for _, s := range ss {
		if maxUp == 0 {
			break
		}
		xs, ys := make([]float64, len(s.Up)), make([]float64, len(s.Up))
		for d, v := range s.Up {
			xs[d], ys[d] = x(d), mid-hUp*v/maxUp
		}
		fmt.Fprintf(&b, `<path d="%s" class="up" stroke="%s" filter="url(#glow)"><title>%s</title></path>`,
			smoothPath(xs, ys), s.Color, template.HTMLEscapeString(s.Name))
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}
