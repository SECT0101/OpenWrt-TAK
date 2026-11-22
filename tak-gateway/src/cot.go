package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

// CoT time format used by TAK.
const cotTime = "2006-01-02T15:04:05.000Z"

// Event is the small slice of a CoT event the gateway needs; Raw is forwarded untouched.
type Event struct {
	Raw      []byte
	UID      string
	Type     string
	Time     string
	Callsign string
	Lat, Lon float64
	HasPoint bool
}

// Key identifies one transmission of an event (for de-duplication).
func (e *Event) Key() string { return e.UID + "|" + e.Type + "|" + e.Time }

// IsPing reports TAK keep-alive / control traffic that must not be relayed.
func (e *Event) IsPing() bool {
	return e.Type == "t-x-c-t" || e.Type == "t-x-c-t-r" || strings.HasPrefix(e.Type, "t-x-takp")
}

// ParseEvent extracts the header fields of one <event> document.
func ParseEvent(raw []byte) (*Event, error) {
	ev := &Event{Raw: raw}
	d := xml.NewDecoder(bytes.NewReader(raw))
	d.Strict = false
	seenEvent := false
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "event":
			seenEvent = true
			for _, a := range se.Attr {
				switch a.Name.Local {
				case "uid":
					ev.UID = a.Value
				case "type":
					ev.Type = a.Value
				case "time":
					ev.Time = a.Value
				}
			}
		case "point":
			for _, a := range se.Attr {
				switch a.Name.Local {
				case "lat":
					fmt.Sscan(a.Value, &ev.Lat)
					ev.HasPoint = true
				case "lon":
					fmt.Sscan(a.Value, &ev.Lon)
				}
			}
		case "contact":
			for _, a := range se.Attr {
				if a.Name.Local == "callsign" {
					ev.Callsign = a.Value
				}
			}
		}
	}
	if !seenEvent || ev.UID == "" {
		return nil, fmt.Errorf("not a CoT event")
	}
	return ev, nil
}

// Splitter cuts a TAK XML stream (events back to back, optional <?xml?> prologs)
// into individual <event> documents.
type Splitter struct {
	buf []byte
	Max int
}

var (
	evOpen  = []byte("<event")
	evClose = []byte("</event>")
)

// Feed appends data and returns every complete event now available.
func (s *Splitter) Feed(p []byte) [][]byte {
	if s.Max == 0 {
		s.Max = 4 << 20
	}
	s.buf = append(s.buf, p...)
	var out [][]byte
	for {
		i := bytes.Index(s.buf, evOpen)
		if i < 0 {
			// keep a short tail in case "<event" is split across reads
			if len(s.buf) > len(evOpen) {
				s.buf = append(s.buf[:0], s.buf[len(s.buf)-len(evOpen):]...)
			}
			return out
		}
		if i > 0 {
			s.buf = s.buf[i:]
		}
		// self-closing <event .../> ?
		gt := bytes.IndexByte(s.buf, '>')
		if gt < 0 {
			break
		}
		if s.buf[gt-1] == '/' {
			out = append(out, clone(s.buf[:gt+1]))
			s.buf = s.buf[gt+1:]
			continue
		}
		j := bytes.Index(s.buf, evClose)
		if j < 0 {
			break
		}
		end := j + len(evClose)
		out = append(out, clone(s.buf[:end]))
		s.buf = s.buf[end:]
	}
	if len(s.buf) > s.Max { // runaway / garbage
		s.buf = s.buf[:0]
	}
	return out
}

func clone(b []byte) []byte { return append([]byte(nil), b...) }

func xmlEsc(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return strings.ReplaceAll(b.String(), `"`, "&quot;")
}

// SelfInfo describes the gateway's own SA marker.
type SelfInfo struct {
	UID, Callsign, Type, Team, Role, Remarks string
	Lat, Lon, Hae                            float64
	CE                                       float64
	Device, OS, Version                      string
	Stale                                    time.Duration
}

// BuildSelf renders the gateway's own position as a CoT SA event.
func BuildSelf(s SelfInfo, now time.Time) []byte {
	stale := s.Stale
	if stale <= 0 {
		stale = 2 * time.Minute
	}
	ce := s.CE
	if ce <= 0 {
		ce = 9999999
	}
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`+
		`<event version="2.0" uid="%s" type="%s" how="m-g" time="%s" start="%s" stale="%s">`+
		`<point lat="%.7f" lon="%.7f" hae="%.1f" ce="%.1f" le="9999999.0"/>`+
		`<detail><contact callsign="%s"/><__group name="%s" role="%s"/>`+
		`<takv device="%s" platform="tak-gateway" os="%s" version="%s"/>`+
		`<precisionlocation geopointsrc="%s" altsrc="%s"/>`+
		`<remarks>%s</remarks></detail></event>`,
		xmlEsc(s.UID), xmlEsc(s.Type), now.UTC().Format(cotTime), now.UTC().Format(cotTime), now.Add(stale).UTC().Format(cotTime),
		s.Lat, s.Lon, s.Hae, ce,
		xmlEsc(s.Callsign), xmlEsc(s.Team), xmlEsc(s.Role),
		xmlEsc(s.Device), xmlEsc(s.OS), xmlEsc(s.Version),
		srcName(s.CE), srcName(s.CE), xmlEsc(s.Remarks)))
}

func srcName(ce float64) string {
	if ce > 0 && ce < 1000 {
		return "GPS"
	}
	return "USER"
}

// BuildPing renders a TAK client keep-alive.
func BuildPing(uid string, now time.Time) []byte {
	t := now.UTC().Format(cotTime)
	st := now.Add(20 * time.Second).UTC().Format(cotTime)
	return []byte(fmt.Sprintf(`<event version="2.0" uid="%s-ping" type="t-x-c-t" how="h-g-i-g-o" time="%s" start="%s" stale="%s">`+
		`<point lat="0.0" lon="0.0" hae="0.0" ce="9999999.0" le="9999999.0"/><detail/></event>`, xmlEsc(uid), t, t, st))
}
