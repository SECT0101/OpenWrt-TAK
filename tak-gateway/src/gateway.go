package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Client is a TAK user seen on the local mesh/LAN.
type Client struct {
	UID      string    `json:"uid"`
	Callsign string    `json:"callsign"`
	Type     string    `json:"type"`
	Lat      float64   `json:"lat"`
	Lon      float64   `json:"lon"`
	IP       string    `json:"ip"`
	Iface    string    `json:"iface"`
	LastSeen time.Time `json:"last_seen"`
}

type Counters struct {
	MeshRx, MeshTx, ServerRx, ServerTx, Bridged, Dupes, Dropped, Beacons atomic.Uint64
}

// Gateway wires the TAK server, the multicast groups and the self beacon together.
type Gateway struct {
	Self       SelfInfo
	Loc        *Locator
	Beacon     time.Duration
	Server     *Server
	SA, Chat   *MeshGroup
	ToServer   bool
	FromServer bool
	Bridge     bool
	TypeFilter []string // server->mesh type prefixes ("" = all)
	StatusPath string

	C       Counters
	started time.Time

	mu       sync.Mutex
	seen     map[string]time.Time // event key -> when (dedupe)
	meshUIDs map[string]time.Time // uids recently heard on the mesh
	clients  map[string]*Client
}

func (g *Gateway) init() {
	g.seen = map[string]time.Time{}
	g.meshUIDs = map[string]time.Time{}
	g.clients = map[string]*Client{}
	g.started = time.Now()
}

// markSeen returns true if the key was already seen within the window.
func (g *Gateway) markSeen(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if t, ok := g.seen[key]; ok && time.Since(t) < 30*time.Second {
		return true
	}
	g.seen[key] = time.Now()
	return false
}

func (g *Gateway) gc() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, t := range g.seen {
		if time.Since(t) > time.Minute {
			delete(g.seen, k)
		}
	}
	for k, t := range g.meshUIDs {
		if time.Since(t) > 2*time.Minute {
			delete(g.meshUIDs, k)
		}
	}
	for k, c := range g.clients {
		if time.Since(c.LastSeen) > 10*time.Minute {
			delete(g.clients, k)
		}
	}
}

func isChat(t string) bool { return strings.HasPrefix(t, "b-t-f") }

// onMesh handles a packet heard on a bridged interface.
func (g *Gateway) onMesh(group *MeshGroup, iface string, src *net.UDPAddr, data []byte) {
	g.C.MeshRx.Add(1)
	raw := data
	if IsTakProtoV1(data) {
		x, err := TakProtoToXML(data)
		if err != nil {
			return // TAK control message or unknown payload
		}
		raw = x
	}
	ev, err := ParseEvent(raw)
	if err != nil || ev.IsPing() || ev.UID == g.Self.UID {
		return
	}
	if g.markSeen(ev.Key()) {
		g.C.Dupes.Add(1)
		return
	}
	g.mu.Lock()
	g.meshUIDs[ev.UID] = time.Now()
	if group == g.SA && strings.HasPrefix(ev.Type, "a-") && ev.HasPoint {
		c := g.clients[ev.UID]
		if c == nil {
			c = &Client{UID: ev.UID}
			g.clients[ev.UID] = c
		}
		c.Type, c.Lat, c.Lon, c.IP, c.Iface, c.LastSeen = ev.Type, ev.Lat, ev.Lon, src.IP.String(), iface, time.Now()
		if ev.Callsign != "" {
			c.Callsign = ev.Callsign
		}
	}
	g.mu.Unlock()
	if g.ToServer && g.Server != nil {
		if g.Server.Send(raw) {
			g.C.ServerTx.Add(1)
		} else {
			g.C.Dropped.Add(1)
		}
	}
	if g.Bridge {
		if n := group.Send(data, iface); n > 0 {
			g.C.Bridged.Add(uint64(n))
		}
	}
}

func (g *Gateway) passFilter(t string) bool {
	if len(g.TypeFilter) == 0 {
		return true
	}
	for _, p := range g.TypeFilter {
		if p == "" || strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// onServer handles an event from the TAK server.
func (g *Gateway) onServer(raw []byte) {
	g.C.ServerRx.Add(1)
	ev, err := ParseEvent(raw)
	if err != nil || ev.IsPing() || ev.UID == g.Self.UID || strings.HasPrefix(ev.Type, "t-x-") {
		return
	}
	if !g.FromServer {
		return
	}
	g.mu.Lock()
	_, local := g.meshUIDs[ev.UID]
	g.mu.Unlock()
	if local { // already on the mesh, don't echo it back
		return
	}
	if g.markSeen(ev.Key()) {
		g.C.Dupes.Add(1)
		return
	}
	target := g.SA
	if isChat(ev.Type) && g.Chat != nil {
		target = g.Chat
	} else if !g.passFilter(ev.Type) {
		return
	}
	if target == nil {
		return
	}
	if n := target.Send(raw, ""); n > 0 {
		g.C.MeshTx.Add(uint64(n))
	}
}

func (g *Gateway) selfEvent() []byte {
	s := g.Self
	p := g.Loc.Current()
	s.Lat, s.Lon, s.Hae, s.CE = p.Lat, p.Lon, p.Hae, p.CE
	g.mu.Lock()
	n := len(g.clients)
	g.mu.Unlock()
	state := "off"
	if g.Server != nil {
		state, _, _ = g.Server.Status()
	}
	s.Remarks = strings.TrimSpace(fmt.Sprintf("%s TAK gateway: %d local TAK user(s), server %s, position %s", s.Remarks, n, state, p.Source))
	s.Stale = 3 * g.Beacon
	return BuildSelf(s, time.Now())
}

func (g *Gateway) beacon() {
	if p := g.Loc.Current(); p.Source == "none" {
		return
	}
	ev := g.selfEvent()
	g.C.Beacons.Add(1)
	if g.Server != nil && g.Server.Send(ev) {
		g.C.ServerTx.Add(1)
	}
	if g.SA != nil {
		g.C.MeshTx.Add(uint64(g.SA.Send(ev, "")))
	}
}

// Status is what the LuCI page (and humans) read from the status file.
type Status struct {
	Version  string            `json:"version"`
	Callsign string            `json:"callsign"`
	UID      string            `json:"uid"`
	Uptime   int64             `json:"uptime"`
	Updated  time.Time         `json:"updated"`
	Server   map[string]any    `json:"server"`
	Position map[string]any    `json:"position"`
	Mesh     map[string]any    `json:"mesh"`
	Counters map[string]uint64 `json:"counters"`
	Clients  []*Client         `json:"clients"`
}

func (g *Gateway) status() Status {
	st := Status{Version: version, Callsign: g.Self.Callsign, UID: g.Self.UID, Uptime: int64(time.Since(g.started).Seconds()), Updated: time.Now().UTC()}
	if g.Server != nil {
		s, e, since := g.Server.Status()
		st.Server = map[string]any{"enabled": true, "address": g.Server.Cfg.Addr(), "protocol": g.Server.Cfg.Proto, "state": s, "error": e, "since": since.UTC()}
	} else {
		st.Server = map[string]any{"enabled": false, "state": "off"}
	}
	p := g.Loc.Current()
	st.Position = map[string]any{"lat": p.Lat, "lon": p.Lon, "hae": p.Hae, "source": p.Source}
	st.Mesh = map[string]any{}
	if g.SA != nil {
		st.Mesh["sa_group"] = g.SA.Group.String()
		st.Mesh["interfaces"] = g.SA.Interfaces()
	}
	if g.Chat != nil {
		st.Mesh["chat_group"] = g.Chat.Group.String()
	}
	st.Counters = map[string]uint64{"mesh_rx": g.C.MeshRx.Load(), "mesh_tx": g.C.MeshTx.Load(), "server_rx": g.C.ServerRx.Load(),
		"server_tx": g.C.ServerTx.Load(), "bridged": g.C.Bridged.Load(), "duplicates": g.C.Dupes.Load(), "dropped": g.C.Dropped.Load(), "beacons": g.C.Beacons.Load()}
	g.mu.Lock()
	for _, c := range g.clients {
		cc := *c
		st.Clients = append(st.Clients, &cc)
	}
	g.mu.Unlock()
	sort.Slice(st.Clients, func(i, j int) bool { return st.Clients[i].Callsign < st.Clients[j].Callsign })
	return st
}

func (g *Gateway) writeStatus() {
	if g.StatusPath == "" {
		return
	}
	b, _ := json.MarshalIndent(g.status(), "", "  ")
	tmp := g.StatusPath + ".tmp"
	os.MkdirAll(filepath.Dir(g.StatusPath), 0755)
	if os.WriteFile(tmp, b, 0644) == nil {
		os.Rename(tmp, g.StatusPath)
	}
}

// Run starts every component and blocks until ctx ends.
func (g *Gateway) Run(ctx context.Context) {
	g.init()
	go g.Loc.Run(ctx)
	if g.Server != nil {
		g.Server.OnEvent = g.onServer
		g.Server.OnUp = func() { go g.beacon() }
		go g.Server.Run(ctx)
	}
	for _, m := range []*MeshGroup{g.SA, g.Chat} {
		if m == nil {
			continue
		}
		mg := m
		mg.OnPacket = func(iface string, src *net.UDPAddr, data []byte) { g.onMesh(mg, iface, src, data) }
		go func() {
			if err := mg.Run(ctx); err != nil {
				log.Printf("%v", err)
			}
		}()
	}
	bt := time.NewTicker(g.Beacon)
	st := time.NewTicker(5 * time.Second)
	gc := time.NewTicker(30 * time.Second)
	defer bt.Stop()
	defer st.Stop()
	defer gc.Stop()
	time.AfterFunc(2*time.Second, g.beacon)
	g.writeStatus()
	for {
		select {
		case <-ctx.Done():
			if g.StatusPath != "" {
				os.Remove(g.StatusPath)
			}
			return
		case <-bt.C:
			g.beacon()
		case <-st.C:
			g.writeStatus()
		case <-gc.C:
			g.gc()
		}
	}
}
