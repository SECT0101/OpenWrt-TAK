package main

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"net"
	"sync"
	"time"
)

// Position is the gateway's current location.
type Position struct {
	Lat, Lon, Hae float64
	CE            float64 // horizontal error (m); 0 = unknown
	Source        string  // "gps", "static" or "none"
	Fix           time.Time
}

// Locator merges a static position with an optional live gpsd feed.
type Locator struct {
	Static   Position
	GpsdAddr string
	MaxAge   time.Duration

	mu   sync.Mutex
	live Position
}

// Current returns the live GPS fix if fresh, otherwise the static position.
func (l *Locator) Current() Position {
	l.mu.Lock()
	defer l.mu.Unlock()
	maxAge := l.MaxAge
	if maxAge == 0 {
		maxAge = 30 * time.Second
	}
	if l.live.Source == "gps" && time.Since(l.live.Fix) < maxAge {
		return l.live
	}
	return l.Static
}

type gpsdTPV struct {
	Class  string  `json:"class"`
	Mode   int     `json:"mode"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	Alt    float64 `json:"alt"`
	AltHAE float64 `json:"altHAE"`
	Epx    float64 `json:"epx"`
	Epy    float64 `json:"epy"`
	Eph    float64 `json:"eph"`
}

// Run follows gpsd (if configured) until ctx ends.
func (l *Locator) Run(ctx context.Context) {
	if l.GpsdAddr == "" {
		return
	}
	for ctx.Err() == nil {
		if err := l.session(ctx); err != nil && ctx.Err() == nil {
			log.Printf("gpsd %s: %v", l.GpsdAddr, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

func (l *Locator) session(ctx context.Context) error {
	d := net.Dialer{Timeout: 5 * time.Second}
	c, err := d.DialContext(ctx, "tcp", l.GpsdAddr)
	if err != nil {
		return err
	}
	defer c.Close()
	go func() { <-ctx.Done(); c.Close() }()
	if _, err := c.Write([]byte(`?WATCH={"enable":true,"json":true};` + "\n")); err != nil {
		return err
	}
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var t gpsdTPV
		if json.Unmarshal(sc.Bytes(), &t) != nil || t.Class != "TPV" || t.Mode < 2 {
			continue
		}
		hae := t.AltHAE
		if hae == 0 {
			hae = t.Alt
		}
		ce := t.Eph
		if ce == 0 && (t.Epx > 0 || t.Epy > 0) {
			ce = (t.Epx + t.Epy) / 2
		}
		if ce == 0 {
			ce = 10
		}
		l.mu.Lock()
		l.live = Position{Lat: t.Lat, Lon: t.Lon, Hae: hae, CE: ce, Source: "gps", Fix: time.Now()}
		l.mu.Unlock()
	}
	return sc.Err()
}
