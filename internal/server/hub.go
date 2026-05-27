package server

// singleHub is a no-auth Hub with one implicit user ("default"). It backs
// the single-user deployment and tests; multi-user uses a real Hub built by
// the composition root.
type singleHub struct {
	tenant *Tenant
}

// SingleUser returns a no-auth Hub exposing one user, "default", bridged to
// the given engine and (optional) history.
func SingleUser(t *Tenant) Hub { return &singleHub{tenant: t} }

const defaultUser = "default"

func (h *singleHub) AuthEnabled() bool                   { return false }
func (h *singleHub) Login(string, string) (string, bool) { return "", false }
func (h *singleHub) Session(string) (string, bool)       { return defaultUser, true }
func (h *singleHub) StartSession(string) (string, int)   { return "", 0 }
func (h *singleHub) EndSession(string)                   {}
func (h *singleHub) Users() []string                     { return []string{defaultUser} }

func (h *singleHub) Tenant(userID string) (*Tenant, bool) {
	if userID == defaultUser {
		return h.tenant, true
	}
	return nil, false
}
