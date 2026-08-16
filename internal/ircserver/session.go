package ircserver

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klippelism/stugan/internal/core"
)

const (
	registrationTimeout = 60 * time.Second
	heartbeatInterval   = 45 * time.Second
	heartbeatPingAfter  = 90 * time.Second
	heartbeatReapAfter  = 240 * time.Second
	maxSaslResponse     = 8 * 1024
)

type echoEntry struct {
	key string
	at  time.Time
}

// Session represents a single downstream IRC client connection to the bouncer.
type Session struct {
	conn       net.Conn
	remoteAddr string
	srv        *Server
	hub        Hub
	engine     *core.Engine
	history    History

	writeMu sync.Mutex
	reader  *bufio.Reader

	caps           map[string]bool
	capNegotiating bool

	saslMech    string
	saslBuf     string
	saslUser    string
	saslNetwork string

	passRaw        string
	clientNick     string
	clientUser     string
	clientRealname string

	registered bool
	closed     atomic.Bool

	userID     string
	networkID  string
	boundNetID string
	isControl  bool

	batchSeq int

	echoMu      sync.Mutex
	pendingEcho []echoEntry

	lastActivity atomic.Int64
	regTimer     *time.Timer
}

func newSession(conn net.Conn, srv *Server, hub Hub) *Session {
	s := &Session{
		conn:       conn,
		remoteAddr: conn.RemoteAddr().String(),
		srv:        srv,
		hub:        hub,
		reader:     bufio.NewReaderSize(conn, maxInputBuffer),
		caps:       make(map[string]bool),
	}
	s.lastActivity.Store(time.Now().Unix())
	s.regTimer = time.AfterFunc(registrationTimeout, func() {
		s.closeWithError("Registration timeout")
	})
	return s
}

func (s *Session) isRegistered() bool {
	return s.registered
}

func (s *Session) write(line string) error {
	if s.closed.Load() {
		return net.ErrClosed
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	clean := strings.ReplaceAll(line, "\r", " ")
	clean = strings.ReplaceAll(clean, "\n", " ")
	clean = strings.ReplaceAll(clean, "\x00", " ")
	_, err := s.conn.Write([]byte(clean + "\r\n"))
	return err
}

func (s *Session) numeric(code string, params ...string) {
	nick := s.currentNickOr("*")
	var allParams []string
	allParams = append(allParams, nick)
	allParams = append(allParams, params...)
	_ = s.write(FormatLine("", serverName, code, allParams...))
}

func (s *Session) notice(text string) {
	nick := s.currentNickOr("*")
	_ = s.write(FormatLine("", serverName, "NOTICE", nick, ":"+text))
}

func (s *Session) currentNick() string {
	if s.engine != nil && s.networkID != "" {
		if netSnapshot := s.engine.SnapshotNetwork(s.networkID); netSnapshot != nil {
			if netSnapshot.Nick != "" {
				return netSnapshot.Nick
			}
		}
	}
	return s.clientNick
}

func (s *Session) currentNickOr(fallback string) string {
	if n := s.currentNick(); n != "" {
		return n
	}
	if s.clientNick != "" {
		return s.clientNick
	}
	return fallback
}

func (s *Session) selfPrefix() string {
	nick := s.currentNickOr("user")
	return fmt.Sprintf("%s!stugan@%s", nick, serverName)
}

func (s *Session) wantsSelfMessages() bool {
	return s.caps[CapSelfMessage] || s.caps[CapEchoMessage]
}

func (s *Session) serve() {
	defer s.destroy("Connection closed")

	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			if err != io.EOF && !s.closed.Load() {
				s.srv.log.Debug("bouncer: client read error", "remote", s.remoteAddr, "err", err)
			}
			return
		}

		s.lastActivity.Store(time.Now().Unix())
		msg := ParseLine(line)
		if msg == nil {
			continue
		}

		if !s.registered {
			s.handlePreRegistration(msg)
		} else {
			s.handleCommand(msg)
		}
	}
}

func (s *Session) handlePreRegistration(msg *Message) {
	switch msg.Command {
	case "CAP":
		s.handleCap(msg)
	case "PASS":
		if len(msg.Params) > 0 {
			s.passRaw = msg.Params[0]
		}
	case "NICK":
		if len(msg.Params) > 0 {
			s.clientNick = CleanNick(msg.Params[0])
		}
	case "USER":
		if len(msg.Params) > 0 {
			s.clientUser = msg.Params[0]
		}
		if len(msg.Params) > 3 {
			s.clientRealname = msg.Params[3]
		}
	case "AUTHENTICATE":
		s.handleSasl(msg)
	case "BOUNCER":
		s.handleBouncerPreReg(msg)
	case "PING":
		token := serverName
		if len(msg.Params) > 0 {
			token = msg.Params[0]
		}
		_ = s.write(FormatLine("", serverName, "PONG", serverName, token))
	case "QUIT":
		s.destroy("Client quit")
		return
	default:
		s.numeric("451", ":You have not registered")
	}

	s.maybeFinishRegistration()
}

func (s *Session) handleCap(msg *Message) {
	sub := ""
	if len(msg.Params) > 0 {
		sub = strings.ToUpper(msg.Params[0])
	}
	nick := s.currentNickOr("*")

	switch sub {
	case "LS":
		if !s.registered {
			s.capNegotiating = true
		}
		version := 301
		if len(msg.Params) > 1 {
			if strings.HasPrefix(msg.Params[1], "302") {
				version = 302
			}
		}
		_ = s.write(FormatLine("", serverName, "CAP", nick, "LS", ":"+capLsList(version)))

	case "LIST":
		var active []string
		for c, ok := range s.caps {
			if ok {
				active = append(active, c)
			}
		}
		_ = s.write(FormatLine("", serverName, "CAP", nick, "LIST", ":"+strings.Join(active, " ")))

	case "REQ":
		if !s.registered {
			s.capNegotiating = true
		}
		reqStr := ""
		if len(msg.Params) > 1 {
			reqStr = msg.Params[1]
		}
		requested := strings.Fields(reqStr)
		allSupported := true
		for _, c := range requested {
			if !isCapSupported(c) {
				allSupported = false
				break
			}
		}
		if !allSupported || len(requested) == 0 {
			_ = s.write(FormatLine("", serverName, "CAP", nick, "NAK", ":"+reqStr))
			return
		}
		for _, c := range requested {
			if strings.HasPrefix(c, "-") {
				delete(s.caps, strings.TrimPrefix(c, "-"))
			} else {
				s.caps[c] = true
			}
		}
		_ = s.write(FormatLine("", serverName, "CAP", nick, "ACK", ":"+reqStr))

	case "END":
		s.capNegotiating = false

	default:
		_ = s.write(FormatLine("", serverName, "410", nick, sub, ":Invalid CAP command"))
	}
}

func (s *Session) handleSasl(msg *Message) {
	nick := s.currentNickOr("*")
	arg := ""
	if len(msg.Params) > 0 {
		arg = msg.Params[0]
	}

	if !s.caps[CapSasl] {
		_ = s.write(FormatLine("", serverName, "904", nick, ":You must request the sasl capability first"))
		return
	}

	if s.saslMech == "" {
		mech := strings.ToUpper(arg)
		found := false
		for _, m := range saslMechanisms {
			if m == mech {
				found = true
				break
			}
		}
		if !found {
			_ = s.write(FormatLine("", serverName, "908", nick, strings.Join(saslMechanisms, ","), ":are available SASL mechanisms"))
			_ = s.write(FormatLine("", serverName, "904", nick, ":SASL authentication failed"))
			return
		}
		s.saslMech = mech
		s.saslBuf = ""
		_ = s.write("AUTHENTICATE +")
		return
	}

	if arg == "*" {
		s.saslMech = ""
		s.saslBuf = ""
		_ = s.write(FormatLine("", serverName, "906", nick, ":SASL authentication aborted"))
		return
	}

	if arg != "+" {
		s.saslBuf += arg
		if len(s.saslBuf) > maxSaslResponse {
			s.saslBuf = ""
			s.saslMech = ""
			_ = s.write(FormatLine("", serverName, "904", nick, ":SASL message too long"))
			return
		}
		if len(arg) == 400 {
			return // more chunks coming
		}
	}

	payload := s.saslBuf
	s.saslBuf = ""
	s.saslMech = ""
	s.finishSaslPlain(payload, nick)
}

func (s *Session) finishSaslPlain(b64 string, nick string) {
	fail := func() {
		_ = s.write(FormatLine("", serverName, "904", nick, ":SASL authentication failed"))
	}

	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		fail()
		return
	}

	parts := strings.Split(string(decoded), "\x00")
	if len(parts) != 3 {
		fail()
		return
	}
	authzid, authcid, passwd := parts[0], parts[1], parts[2]
	loginIdent := authcid
	if loginIdent == "" {
		loginIdent = authzid
	}
	parsed := UnmarshalLogin(loginIdent)
	if parsed.Username == "" || passwd == "" {
		fail()
		return
	}

	userID, ok := s.hub.Login(parsed.Username, passwd)
	if !ok {
		fail()
		return
	}

	s.saslUser = userID
	s.saslNetwork = parsed.Network
	_ = s.write(FormatLine("", serverName, "900", nick, fmt.Sprintf("%s!stugan@%s", nick, serverName), userID, ":You are now logged in as "+userID))
	_ = s.write(FormatLine("", serverName, "903", nick, ":SASL authentication successful"))
}

func (s *Session) handleBouncerPreReg(msg *Message) {
	if !s.caps[CapBouncerNetworks] {
		_ = s.write(FormatLine("", serverName, "FAIL", "BOUNCER", "UNKNOWN_COMMAND", ":Negotiate soju.im/bouncer-networks capability first"))
		return
	}
	sub := ""
	if len(msg.Params) > 0 {
		sub = strings.ToUpper(msg.Params[0])
	}
	if sub != "BIND" {
		_ = s.write(FormatLine("", serverName, "FAIL", "BOUNCER", "UNKNOWN_COMMAND", sub, ":Unknown subcommand"))
		return
	}
	if s.saslUser == "" && s.passRaw == "" && s.hub.AuthEnabled() {
		_ = s.write(FormatLine("", serverName, "FAIL", "BOUNCER", "ACCOUNT_REQUIRED", "BIND", ":Authentication needed to bind to bouncer network"))
		return
	}
	if len(msg.Params) > 1 {
		s.boundNetID = msg.Params[1]
	}
}

func (s *Session) maybeFinishRegistration() {
	if s.registered || s.closed.Load() {
		return
	}
	if s.clientNick == "" || s.clientUser == "" || s.capNegotiating {
		return
	}
	s.authenticate()
}

func (s *Session) failRegistration(text string) {
	s.srv.log.Warn("bouncer: registration failed", "remote", s.remoteAddr, "reason", text)
	_ = s.write(FormatLine("", serverName, "464", s.currentNickOr("*"), ":"+text))
	s.closeWithError(text)
}

func (s *Session) authenticate() {
	if s.saslUser != "" {
		netSel := s.saslNetwork
		if netSel == "" && s.clientUser != "" {
			netSel = UnmarshalLogin(s.clientUser).Network
		}
		s.completeAttach(s.saslUser, netSel)
		return
	}

	if !s.hub.AuthEnabled() {
		// Single-user unauthenticated mode
		users := s.hub.Users()
		userID := "default"
		if len(users) > 0 {
			userID = users[0]
		}
		netSel := ""
		if s.passRaw != "" {
			if creds, ok := ParseCredentials(s.passRaw, s.clientUser); ok {
				netSel = creds.Network
			}
		} else if s.clientUser != "" {
			netSel = UnmarshalLogin(s.clientUser).Network
		}
		s.completeAttach(userID, netSel)
		return
	}

	if s.passRaw == "" {
		s.failRegistration("Password required: PASS <username>[/<network>]:<password>")
		return
	}

	creds, ok := ParseCredentials(s.passRaw, s.clientUser)
	if !ok || creds.Username == "" {
		s.failRegistration("Invalid credentials format: PASS <username>[/<network>]:<password>")
		return
	}

	userID, ok := s.hub.Login(creds.Username, creds.Secret)
	if !ok {
		s.failRegistration("Invalid username or password")
		return
	}

	s.completeAttach(userID, creds.Network)
}

func (s *Session) completeAttach(userID string, networkSel string) {
	tenant, ok := s.hub.Tenant(userID)
	if !ok || tenant == nil {
		s.failRegistration("User account not found")
		return
	}

	userSnapshot := tenant.Engine.Snapshot()
	if userSnapshot == nil {
		s.failRegistration("User state unavailable")
		return
	}

	// Control mode (unbound)
	hasSelector := s.boundNetID != "" || networkSel != ""
	if !hasSelector && s.caps[CapBouncerNetworks] {
		s.registerControl(userID)
		return
	}

	networks := userSnapshot.Networks
	if len(networks) == 0 {
		s.failRegistration("No IRC networks configured — add one in the web UI first")
		return
	}

	var targetNet *core.Network
	if s.boundNetID != "" {
		for _, n := range networks {
			if n.ID == s.boundNetID || n.Name == s.boundNetID {
				targetNet = n
				break
			}
		}
		if targetNet == nil {
			_ = s.write(FormatLine("", serverName, "FAIL", "BOUNCER", "INVALID_NETID", s.boundNetID, ":Unknown network ID"))
			s.closeWithError("Unknown network ID")
			return
		}
	} else if networkSel != "" {
		selLower := strings.ToLower(networkSel)
		for _, n := range networks {
			if strings.ToLower(n.Name) == selLower || strings.ToLower(n.ID) == selLower {
				targetNet = n
				break
			}
		}
		if targetNet == nil {
			var avail []string
			for _, n := range networks {
				avail = append(avail, n.Name)
			}
			s.failRegistration(fmt.Sprintf("Unknown network '%s' — available: %s", networkSel, strings.Join(avail, ", ")))
			return
		}
	} else if len(networks) == 1 {
		targetNet = networks[0]
	} else {
		var avail []string
		for _, n := range networks {
			avail = append(avail, n.Name)
		}
		s.failRegistration(fmt.Sprintf("Multiple networks — log in as %s/<network>. Available: %s", userID, strings.Join(avail, ", ")))
		return
	}

	s.bindNetwork(userID, targetNet)
}

func (s *Session) bindNetwork(userID string, network *core.Network) {
	tenant, _ := s.hub.Tenant(userID)
	s.userID = userID
	s.networkID = network.ID
	s.engine = tenant.Engine
	s.history = tenant.History
	s.registered = true

	if s.regTimer != nil {
		s.regTimer.Stop()
		s.regTimer = nil
	}

	s.srv.reg.add(s)
	s.sendAttachBurst()
	s.srv.log.Info("bouncer: client attached", "user", userID, "network", network.Name, "remote", s.remoteAddr)
}

func (s *Session) registerControl(userID string) {
	tenant, _ := s.hub.Tenant(userID)
	s.userID = userID
	s.engine = tenant.Engine
	s.history = tenant.History
	s.isControl = true
	s.registered = true

	if s.regTimer != nil {
		s.regTimer.Stop()
		s.regTimer = nil
	}

	s.srv.reg.add(s)
	s.sendControlBurst()
	s.srv.log.Info("bouncer: control connection registered", "user", userID, "remote", s.remoteAddr)
}

func (s *Session) sendAttachBurst() {
	liveNick := s.currentNick()
	requestedNick := s.clientNick
	if requestedNick == "" {
		requestedNick = liveNick
	}
	if requestedNick == "" {
		requestedNick = "user"
	}

	// 001-004 Welcome numerics
	_ = s.write(FormatLine("", serverName, "001", requestedNick, fmt.Sprintf(":Welcome to the stugan bouncer, %s", requestedNick)))
	_ = s.write(FormatLine("", serverName, "002", requestedNick, fmt.Sprintf(":Your host is %s, running stugan", serverName)))
	_ = s.write(FormatLine("", serverName, "003", requestedNick, ":This server was created for you"))
	_ = s.write(FormatLine("", serverName, "004", requestedNick, serverName, "stugan", "o", "o"))

	// If client asked for a nick different from the network's current live nick, emit NICK transition
	if liveNick != "" && liveNick != requestedNick {
		_ = s.write(FormatLine("", fmt.Sprintf("%s!stugan@%s", requestedNick, serverName), "NICK", ":"+liveNick))
	}

	// 005 ISUPPORT
	tokens := defaultISupportTokens()
	if s.caps[CapBouncerNetworks] {
		tokens = append(tokens, fmt.Sprintf("BOUNCER_NETID=%s", s.networkID))
	}
	if s.caps[CapChatHistory] {
		tokens = append(tokens, fmt.Sprintf("CHATHISTORY=%d", maxChatHistory), "MSGREFTYPES=timestamp")
	}
	tokens = append(tokens, ":are supported by this server")
	var isupportArgs []string
	isupportArgs = append(isupportArgs, liveNick)
	isupportArgs = append(isupportArgs, tokens...)
	_ = s.write(FormatLine("", serverName, "005", isupportArgs...))

	_ = s.write(FormatLine("", serverName, "422", liveNick, ":MOTD File is missing"))

	netSnapshot := s.engine.SnapshotNetwork(s.networkID)
	if netSnapshot == nil || netSnapshot.State != core.StateRegistered {
		s.notice("Network is currently connecting/disconnected; channels will appear once connected.")
	} else {
		s.sendJoinBurst(netSnapshot)
		s.sendPlayback(netSnapshot)
	}

	if s.caps[CapBouncerNetworksNotify] {
		s.sendNetworkList()
	}
}

func (s *Session) sendControlBurst() {
	nick := s.currentNickOr("user")
	_ = s.write(FormatLine("", serverName, "001", nick, fmt.Sprintf(":Welcome to the stugan bouncer, %s", nick)))
	_ = s.write(FormatLine("", serverName, "002", nick, fmt.Sprintf(":Your host is %s, running stugan", serverName)))
	_ = s.write(FormatLine("", serverName, "003", nick, ":This server was created for you"))
	_ = s.write(FormatLine("", serverName, "004", nick, serverName, "stugan", "o", "o"))
	_ = s.write(FormatLine("", serverName, "005", nick, "NETWORK=stugan", "CASEMAPPING=rfc1459", ":are supported by this server"))
	_ = s.write(FormatLine("", serverName, "422", nick, ":MOTD File is missing"))

	if s.caps[CapBouncerNetworksNotify] {
		s.sendNetworkList()
	}
}

func (s *Session) sendJoinBurst(n *core.Network) {
	nick := s.currentNickOr("*")
	prefix := s.selfPrefix()

	for _, ch := range n.Channels {
		if ch.Kind != core.KindChannel {
			continue
		}
		_ = s.write(FormatLine("", prefix, "JOIN", ch.Name))
		if ch.Topic != "" {
			_ = s.write(FormatLine("", serverName, "332", nick, ch.Name, ch.Topic))
			if ch.TopicSetter != "" {
				setterTime := strconv.FormatInt(ch.TopicTime.Unix(), 10)
				_ = s.write(FormatLine("", serverName, "333", nick, ch.Name, ch.TopicSetter, setterTime))
			}
		}

		var memberNames []string
		for _, m := range ch.Members {
			sym := MemberPrefixSymbol(m.Modes)
			memberNames = append(memberNames, sym+m.Nick)
		}
		for _, line := range BuildNamesLines(nick, ch.Name, memberNames) {
			_ = s.write(line)
		}
	}
}

func (s *Session) sendPlayback(n *core.Network) {
	if s.history == nil {
		return
	}
	limit := s.srv.opts.MaxPlayback
	if limit <= 0 {
		limit = defaultMaxPlayback
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, ch := range n.Channels {
		msgs, _, err := s.history.Backlog(ctx, s.networkID, ch.Name, 0, limit)
		if err != nil {
			continue
		}
		isChannel := ch.Kind == core.KindChannel
		for _, m := range msgs {
			lines := s.formatPlaybackMessage(m, isChannel, "")
			for _, line := range lines {
				_ = s.write(line)
			}
		}
	}
}

func (s *Session) withBatch(batchType string, params []string, fn func(ref string)) {
	ref := ""
	if s.caps[CapBatch] {
		s.batchSeq++
		ref = fmt.Sprintf("b%d", s.batchSeq)
		var bParams []string
		bParams = append(bParams, "+"+ref, batchType)
		bParams = append(bParams, params...)
		_ = s.write(FormatLine("", serverName, "BATCH", bParams...))
	}

	fn(ref)

	if ref != "" {
		_ = s.write(FormatLine("", serverName, "BATCH", "-"+ref))
	}
}

func (s *Session) sendNetworkList() {
	if s.engine == nil {
		return
	}
	u := s.engine.Snapshot()
	if u == nil {
		return
	}

	s.withBatch("soju.im/bouncer-networks", nil, func(ref string) {
		tag := ""
		if ref != "" {
			tag = fmt.Sprintf("batch=%s", ref)
		}
		for _, netw := range u.Networks {
			state := "disconnected"
			if netw.State == core.StateRegistered {
				state = "connected"
			} else if netw.State == core.StateConnecting {
				state = "connecting"
			}
			tlsFlag := "0"
			if netw.Params.TLS {
				tlsFlag = "1"
			}
			host := netw.Params.Addr
			port := "6667"
			if h, p, err := net.SplitHostPort(netw.Params.Addr); err == nil {
				host = h
				port = p
			}
			attrs := fmt.Sprintf("name=%s;state=%s;host=%s;port=%s;tls=%s;nickname=%s",
				EscapeTagValue(netw.Name),
				state,
				EscapeTagValue(host),
				port,
				tlsFlag,
				EscapeTagValue(netw.Nick),
			)
			_ = s.write(FormatLine(tag, serverName, "BOUNCER", "NETWORK", netw.ID, attrs))
		}
	})
}

func (s *Session) registerEcho(kind core.MsgKind, target, text string) {
	key := fmt.Sprintf("%s\x00%s\x00%s", kind, core.FoldIRC(target), text)
	s.echoMu.Lock()
	defer s.echoMu.Unlock()
	cutoff := time.Now().Add(-30 * time.Second)
	var active []echoEntry
	for _, e := range s.pendingEcho {
		if e.at.After(cutoff) {
			active = append(active, e)
		}
	}
	active = append(active, echoEntry{key: key, at: time.Now()})
	if len(active) > 200 {
		active = active[len(active)-200:]
	}
	s.pendingEcho = active
}

func (s *Session) isOwnEcho(kind core.MsgKind, target, text string) bool {
	key := fmt.Sprintf("%s\x00%s\x00%s", kind, core.FoldIRC(target), text)
	s.echoMu.Lock()
	defer s.echoMu.Unlock()
	for i, e := range s.pendingEcho {
		if e.key == key {
			// consume echo key
			s.pendingEcho = append(s.pendingEcho[:i], s.pendingEcho[i+1:]...)
			return true
		}
	}
	return false
}

func (s *Session) heartbeat(now time.Time) {
	if s.closed.Load() {
		return
	}
	last := time.Unix(s.lastActivity.Load(), 0)
	idle := now.Sub(last)
	if idle > heartbeatReapAfter {
		s.closeWithError("Ping timeout")
	} else if idle > heartbeatPingAfter {
		_ = s.write(fmt.Sprintf("PING :%s", serverName))
	}
}

func (s *Session) closeWithError(reason string) {
	_ = s.write(fmt.Sprintf("ERROR :%s", reason))
	s.destroy(reason)
}

func (s *Session) destroy(reason string) {
	if s.closed.Swap(true) {
		return
	}
	if s.regTimer != nil {
		s.regTimer.Stop()
		s.regTimer = nil
	}
	s.srv.reg.remove(s)
	_ = s.conn.Close()
	if s.registered {
		s.srv.log.Info("bouncer: client detached", "user", s.userID, "network", s.networkID, "remote", s.remoteAddr, "reason", reason)
	}
}
