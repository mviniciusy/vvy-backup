package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// RC fala com o `rclone rcd` pela API HTTP (https://rclone.org/rc/).
type RC struct {
	URL, User, Pass string
	c               http.Client
}

func (r *RC) Call(path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", r.URL+"/"+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.SetBasicAuth(r.User, r.Pass)
	req.Header.Set("Content-Type", "application/json")
	r.c.Timeout = 10 * time.Minute // operations/size em pastas grandes demora
	resp, err := r.c.Do(req)
	if err != nil {
		return fmt.Errorf("rclone indisponível: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error string }
		if json.Unmarshal(body, &e) != nil || e.Error == "" {
			e.Error = string(body)
		}
		return fmt.Errorf("rclone %s: %s", path, e.Error)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

type JobStatus struct {
	Finished bool   `json:"finished"`
	Success  bool   `json:"success"`
	Error    string `json:"error"`
}

type Transfer struct {
	Name       string  `json:"name"`
	Percentage int     `json:"percentage"`
	Speed      float64 `json:"speed"`
}

type Stats struct {
	Bytes          int64      `json:"bytes"`
	TotalBytes     int64      `json:"totalBytes"`
	Speed          float64    `json:"speed"`
	Transfers      int64      `json:"transfers"`
	TotalTransfers int64      `json:"totalTransfers"`
	Checks         int64      `json:"checks"`
	TotalChecks    int64      `json:"totalChecks"`
	Deletes        int64      `json:"deletes"`
	Renames        int64      `json:"renames"`
	Errors         int64      `json:"errors"`
	LastError      string     `json:"lastError"`
	Transferring   []Transfer `json:"transferring"`
}
