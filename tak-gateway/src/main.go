// tak-gateway: bridges TAK SA multicast on OpenWrt / Hak5 devices to a TAK Server
// and puts the device itself on the map.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error {
	for _, p := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }) {
		*l = append(*l, p)
	}
	return nil
}

func hostDevice() string {
	if b, err := os.ReadFile("/tmp/sysinfo/model"); err == nil {
		return strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile("/proc/device-tree/model"); err == nil {
		return strings.TrimRight(strings.TrimSpace(string(b)), "\x00")
	}
	return runtime.GOARCH
}

func hostOS() string {
	if b, err := os.ReadFile("/etc/openwrt_release"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "DISTRIB_DESCRIPTION=") {
				return strings.Trim(strings.TrimPrefix(l, "DISTRIB_DESCRIPTION="), `'"`)
			}
		}
	}
	return "Linux"
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "import":
			os.Exit(cmdImport(os.Args[2:]))
		case "version", "-version", "--version":
			fmt.Println("tak-gateway", version)
			return
		}
	}
	hostname, _ := os.Hostname()
	var (
		callsign = flag.String("callsign", hostname, "callsign shown on the TAK map")
		uid      = flag.String("uid", "", "CoT uid (default tak-gateway-<hostname>)")
		cotType  = flag.String("type", "a-f-G-U-C", "CoT type for the gateway marker")
		team     = flag.String("team", "Cyan", "team color")
		role     = flag.String("role", "Team Member", "team role")
		remarks  = flag.String("remarks", "", "extra text for the marker remarks")
		lat      = flag.Float64("lat", 0, "static latitude")
		lon      = flag.Float64("lon", 0, "static longitude")
		hae      = flag.Float64("hae", 0, "static height (m)")
		gpsd     = flag.String("gpsd", "", "gpsd address for a live position, e.g. 127.0.0.1:2947")
		beacon   = flag.Duration("beacon", 30*time.Second, "how often to send the gateway's own position")

		srvHost  = flag.String("server", "", "TAK Server host (empty = mesh only)")
		srvPort  = flag.Int("port", 8089, "TAK Server streaming port")
		srvProto = flag.String("proto", "ssl", "ssl or tcp")
		cert     = flag.String("cert", "", "client certificate (PEM)")
		key      = flag.String("key", "", "client key (PEM)")
		ca       = flag.String("ca", "", "server CA / truststore (PEM)")
		insecure = flag.Bool("insecure", false, "do not verify the server certificate")

		saGroup    = flag.String("sa-group", "239.2.3.1:6969", "SA multicast group")
		chatGroup  = flag.String("chat-group", "224.10.10.1:17012", "GeoChat multicast group")
		chat       = flag.Bool("chat", true, "bridge GeoChat multicast")
		toServer   = flag.Bool("to-server", true, "send mesh traffic to the TAK Server")
		fromServer = flag.Bool("from-server", true, "send TAK Server traffic to the mesh")
		bridge     = flag.Bool("bridge", true, "relay multicast between the bridged interfaces")
		ttl        = flag.Int("ttl", 1, "multicast TTL")
		status     = flag.String("status", "/var/run/tak-gateway.json", "status file ('' = none)")
		verbose    = flag.Bool("v", false, "log more")
	)
	var ifaces, filter listFlag
	flag.Var(&ifaces, "iface", "interface to bridge (repeat or comma list), e.g. br-lan")
	flag.Var(&filter, "from-server-types", "only send these CoT type prefixes from the server to the mesh (default all)")
	flag.Parse()
	log.SetFlags(0)
	if !*verbose {
		log.SetFlags(0)
	}
	if *uid == "" {
		*uid = "tak-gateway-" + hostname
	}
	if *beacon < 5*time.Second {
		*beacon = 5 * time.Second
	}
	log.Printf("tak-gateway %s starting as %q (%s)", version, *callsign, *uid)

	src := "static"
	if *lat == 0 && *lon == 0 {
		src = "none"
	}
	gw := &Gateway{
		Self: SelfInfo{UID: *uid, Callsign: *callsign, Type: *cotType, Team: *team, Role: *role, Remarks: *remarks,
			Device: hostDevice(), OS: hostOS(), Version: version},
		Loc:        &Locator{Static: Position{Lat: *lat, Lon: *lon, Hae: *hae, Source: src}, GpsdAddr: *gpsd},
		Beacon:     *beacon,
		ToServer:   *toServer,
		FromServer: *fromServer,
		Bridge:     *bridge,
		TypeFilter: filter,
		StatusPath: *status,
	}
	if *srvHost != "" {
		if *srvProto != "ssl" && *srvProto != "tcp" {
			log.Fatalf("-proto must be ssl or tcp")
		}
		gw.Server = NewServer(ServerConfig{Host: *srvHost, Port: *srvPort, Proto: *srvProto, Cert: *cert, Key: *key, CA: *ca, Insecure: *insecure}, *uid)
	}
	if len(ifaces) > 0 {
		var err error
		if gw.SA, err = NewMeshGroup("sa", *saGroup, ifaces, *ttl); err != nil {
			log.Fatalf("sa-group: %v", err)
		}
		if *chat {
			if gw.Chat, err = NewMeshGroup("chat", *chatGroup, ifaces, *ttl); err != nil {
				log.Fatalf("chat-group: %v", err)
			}
		}
	}
	if gw.Server == nil && gw.SA == nil {
		log.Fatalf("nothing to do: set -server and/or -iface")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	gw.Run(ctx)
	log.Printf("tak-gateway stopped")
}
