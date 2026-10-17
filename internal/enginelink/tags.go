package enginelink

import (
	"strconv"
	"strings"
	"time"
)

// Tag keys stugan's relay adds to lines it hands the IRC client. They never
// come from the server: any incoming tag under this vendor prefix is stripped
// first, so a server can't forge a sequence number or a replay marker.
const (
	TagPrefix = "stugan/"
	// TagSeq carries the engine's sequence number of a relayed line; the IRC
	// layer acks it once the line's events are committed.
	TagSeq = "stugan/seq"
	// TagReplay marks a line from the attach replay: state for the client,
	// not news for the user.
	TagReplay = "stugan/replay"
	// TagAck carries the seq an ack probe vouches for (see AckProbe).
	TagAck = "stugan/ack"
	// AckProbeCommand is the command of an ack probe line. No IRC software
	// sends it and the IRC layer ignores it beyond reading TagAck.
	AckProbeCommand = "STUGANACK"
)

// AckProbe is the line (CRLF included) the relay hands the client once it
// has read everything through seq.
func AckProbe(seq uint64) string {
	return "@" + TagAck + "=" + strconv.FormatUint(seq, 10) + " :stugan " + AckProbeCommand + "\r\n"
}

// serverTimeFormat is the IRCv3 server-time format.
const serverTimeFormat = "2006-01-02T15:04:05.000Z"

// TagLine returns line with stugan's tags added: TagSeq (seq > 0), TagReplay
// (replay), and a server-time `time` tag from at (unix ms, > 0) when the line
// has none — so a line that waited in the engine's buffer keeps the time it
// actually arrived, not the time stugan got round to reading it.
func TagLine(line string, seq uint64, at int64, replay bool) string {
	var tags []string
	rest := line
	hasTime := false
	if strings.HasPrefix(line, "@") {
		i := strings.IndexByte(line, ' ')
		if i < 0 {
			return line
		}
		for t := range strings.SplitSeq(line[1:i], ";") {
			key, _, _ := strings.Cut(t, "=")
			if t == "" || strings.HasPrefix(key, TagPrefix) {
				continue
			}
			if key == "time" {
				hasTime = true
			}
			tags = append(tags, t)
		}
		rest = strings.TrimLeft(line[i+1:], " ")
	}
	var ours []string
	if seq > 0 {
		ours = append(ours, TagSeq+"="+strconv.FormatUint(seq, 10))
	}
	if replay {
		ours = append(ours, TagReplay)
	}
	if !hasTime && at > 0 {
		ours = append(ours, "time="+time.UnixMilli(at).UTC().Format(serverTimeFormat))
	}
	tags = append(ours, tags...)
	if len(tags) == 0 {
		return rest
	}
	return "@" + strings.Join(tags, ";") + " " + rest
}
