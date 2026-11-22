package main

// Minimal TAK Protocol v1 (protobuf) decoder for mesh SA packets.
// Mesh packet = 0xbf 0x01 0xbf + TakMessage. We only need TakMessage.cotEvent,
// which we turn back into CoT XML so the TAK server and any client can use it.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

var errProto = errors.New("bad protobuf")

type pbField struct {
	num   int
	wire  int
	u     uint64
	bytes []byte
}

func pbParse(b []byte) ([]pbField, error) {
	var out []pbField
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, errProto
		}
		b = b[n:]
		f := pbField{num: int(key >> 3), wire: int(key & 7)}
		switch f.wire {
		case 0:
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, errProto
			}
			f.u, b = v, b[n:]
		case 1:
			if len(b) < 8 {
				return nil, errProto
			}
			f.u, b = binary.LittleEndian.Uint64(b), b[8:]
		case 2:
			l, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b)-n) < l {
				return nil, errProto
			}
			f.bytes, b = b[n:n+int(l)], b[n+int(l):]
		case 5:
			if len(b) < 4 {
				return nil, errProto
			}
			f.u, b = uint64(binary.LittleEndian.Uint32(b)), b[4:]
		default:
			return nil, errProto
		}
		out = append(out, f)
	}
	return out, nil
}

func pbDouble(f pbField) float64 { return math.Float64frombits(f.u) }

// IsTakProtoV1 reports whether a mesh packet uses the TAK protocol v1 header.
func IsTakProtoV1(p []byte) bool { return len(p) > 3 && p[0] == 0xbf && p[1] == 0x01 && p[2] == 0xbf }

// TakProtoToXML converts a v1 mesh packet into a CoT XML event.
func TakProtoToXML(p []byte) ([]byte, error) {
	if !IsTakProtoV1(p) {
		return nil, errors.New("not TAK protocol v1")
	}
	msg, err := pbParse(p[3:])
	if err != nil {
		return nil, err
	}
	var ev []byte
	for _, f := range msg {
		if f.num == 2 && f.wire == 2 {
			ev = f.bytes
		}
	}
	if ev == nil {
		return nil, errors.New("no cotEvent (control message)")
	}
	fs, err := pbParse(ev)
	if err != nil {
		return nil, err
	}
	var (
		typ, access, qos, opex, uid, how string
		send, start, stale               uint64
		lat, lon, hae, ce, le            float64
		detail                           []byte
	)
	ce, le = 9999999, 9999999
	for _, f := range fs {
		switch f.num {
		case 1:
			typ = string(f.bytes)
		case 2:
			access = string(f.bytes)
		case 3:
			qos = string(f.bytes)
		case 4:
			opex = string(f.bytes)
		case 5:
			uid = string(f.bytes)
		case 6:
			send = f.u
		case 7:
			start = f.u
		case 8:
			stale = f.u
		case 9:
			how = string(f.bytes)
		case 10:
			lat = pbDouble(f)
		case 11:
			lon = pbDouble(f)
		case 12:
			hae = pbDouble(f)
		case 13:
			ce = pbDouble(f)
		case 14:
			le = pbDouble(f)
		case 15:
			detail = f.bytes
		}
	}
	if uid == "" || typ == "" {
		return nil, errors.New("cotEvent missing uid/type")
	}
	ms := func(v uint64) string { return time.UnixMilli(int64(v)).UTC().Format(cotTime) }
	var sb strings.Builder
	fmt.Fprintf(&sb, `<event version="2.0" uid="%s" type="%s" how="%s" time="%s" start="%s" stale="%s"`,
		xmlEsc(uid), xmlEsc(typ), xmlEsc(how), ms(send), ms(start), ms(stale))
	if access != "" {
		fmt.Fprintf(&sb, ` access="%s"`, xmlEsc(access))
	}
	if qos != "" {
		fmt.Fprintf(&sb, ` qos="%s"`, xmlEsc(qos))
	}
	if opex != "" {
		fmt.Fprintf(&sb, ` opex="%s"`, xmlEsc(opex))
	}
	fmt.Fprintf(&sb, `><point lat="%.7f" lon="%.7f" hae="%.1f" ce="%.1f" le="%.1f"/><detail>`, lat, lon, hae, ce, le)
	if detail != nil {
		if err := detailXML(&sb, detail); err != nil {
			return nil, err
		}
	}
	sb.WriteString(`</detail></event>`)
	return []byte(sb.String()), nil
}

func detailXML(sb *strings.Builder, b []byte) error {
	fs, err := pbParse(b)
	if err != nil {
		return err
	}
	for _, f := range fs {
		if f.wire != 2 {
			continue
		}
		switch f.num {
		case 1: // xmlDetail: raw XML children of <detail>
			sb.Write(f.bytes)
		case 2: // contact
			sub, err := pbParse(f.bytes)
			if err != nil {
				return err
			}
			sb.WriteString(`<contact`)
			for _, s := range sub {
				switch s.num {
				case 1:
					fmt.Fprintf(sb, ` endpoint="%s"`, xmlEsc(string(s.bytes)))
				case 2:
					fmt.Fprintf(sb, ` callsign="%s"`, xmlEsc(string(s.bytes)))
				}
			}
			sb.WriteString(`/>`)
		case 3: // group
			sub, err := pbParse(f.bytes)
			if err != nil {
				return err
			}
			sb.WriteString(`<__group`)
			for _, s := range sub {
				switch s.num {
				case 1:
					fmt.Fprintf(sb, ` name="%s"`, xmlEsc(string(s.bytes)))
				case 2:
					fmt.Fprintf(sb, ` role="%s"`, xmlEsc(string(s.bytes)))
				}
			}
			sb.WriteString(`/>`)
		case 4: // precisionLocation
			sub, err := pbParse(f.bytes)
			if err != nil {
				return err
			}
			sb.WriteString(`<precisionlocation`)
			for _, s := range sub {
				switch s.num {
				case 1:
					fmt.Fprintf(sb, ` geopointsrc="%s"`, xmlEsc(string(s.bytes)))
				case 2:
					fmt.Fprintf(sb, ` altsrc="%s"`, xmlEsc(string(s.bytes)))
				}
			}
			sb.WriteString(`/>`)
		case 5: // status
			sub, err := pbParse(f.bytes)
			if err != nil {
				return err
			}
			for _, s := range sub {
				if s.num == 1 {
					fmt.Fprintf(sb, `<status battery="%d"/>`, s.u)
				}
			}
		case 6: // takv
			sub, err := pbParse(f.bytes)
			if err != nil {
				return err
			}
			sb.WriteString(`<takv`)
			names := map[int]string{1: "device", 2: "platform", 3: "os", 4: "version"}
			for _, s := range sub {
				if n, ok := names[s.num]; ok {
					fmt.Fprintf(sb, ` %s="%s"`, n, xmlEsc(string(s.bytes)))
				}
			}
			sb.WriteString(`/>`)
		case 7: // track
			sub, err := pbParse(f.bytes)
			if err != nil {
				return err
			}
			var spd, crs float64
			for _, s := range sub {
				switch s.num {
				case 1:
					spd = pbDouble(s)
				case 2:
					crs = pbDouble(s)
				}
			}
			fmt.Fprintf(sb, `<track speed="%.2f" course="%.2f"/>`, spd, crs)
		}
	}
	return nil
}

// ---- encoder (used by tests to build realistic v1 packets) ----

func pbKey(num, wire int) []byte { return binary.AppendUvarint(nil, uint64(num<<3|wire)) }

func pbStr(num int, s string) []byte {
	b := pbKey(num, 2)
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

func pbBytes(num int, v []byte) []byte { return pbStr(num, string(v)) }

func pbVarint(num int, v uint64) []byte { return binary.AppendUvarint(pbKey(num, 0), v) }

func pbDbl(num int, v float64) []byte {
	return binary.LittleEndian.AppendUint64(pbKey(num, 1), math.Float64bits(v))
}
