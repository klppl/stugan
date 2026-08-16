package ircserver

import (
	"fmt"
	"strings"
)

const (
	CapSasl                  = "sasl"
	CapServerTime            = "server-time"
	CapMessageTags           = "message-tags"
	CapEchoMessage           = "echo-message"
	CapSelfMessage           = "znc.in/self-message"
	CapBatch                 = "batch"
	CapChatHistory           = "draft/chathistory"
	CapBouncerNetworks       = "soju.im/bouncer-networks"
	CapBouncerNetworksNotify = "soju.im/bouncer-networks-notify"
	CapReadMarker            = "draft/read-marker"
	CapReadMarkerAlt         = "read-marker"

	maxChatHistory = 1000
)

var supportedCaps = []string{
	CapSasl,
	CapServerTime,
	CapMessageTags,
	CapEchoMessage,
	CapSelfMessage,
	CapBatch,
	CapChatHistory,
	CapBouncerNetworks,
	CapBouncerNetworksNotify,
	CapReadMarker,
	CapReadMarkerAlt,
}

var saslMechanisms = []string{"PLAIN"}

func isCapSupported(capName string) bool {
	c := strings.TrimPrefix(capName, "-")
	for _, s := range supportedCaps {
		if s == c {
			return true
		}
	}
	return false
}

// capLsList returns the CAP LS space-separated token list.
// If version >= 302, SASL advertised mechanism list is included (sasl=PLAIN).
func capLsList(version int) string {
	var out []string
	for _, c := range supportedCaps {
		if c == CapSasl && version >= 302 {
			out = append(out, fmt.Sprintf("sasl=%s", strings.Join(saslMechanisms, ",")))
		} else {
			out = append(out, c)
		}
	}
	return strings.Join(out, " ")
}

// defaultISupportTokens returns standard 005 numeric tokens for downstream clients.
func defaultISupportTokens() []string {
	return []string{
		"CHANTYPES=#&",
		"PREFIX=(ov)@+",
		"CASEMAPPING=rfc1459",
		"NICKLEN=32",
		"CHANNELLEN=64",
		"NETWORK=stugan",
	}
}
