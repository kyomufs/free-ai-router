package control

import (
	"net/http"
	"sort"

	"free-ai-router/internal/config"
	"free-ai-router/internal/egress"
	"free-ai-router/internal/provider"
	"free-ai-router/internal/state"
)

// Status — a snapshot of the daemon's state.
//
// A snapshot, not a live object: the response must be consistent across its
// fields. By handing out live structures, the gateway could show the registry
// before a change and the counters after it.
type Status struct {
	// Mode — the deployment shape.
	Mode string `json:"mode"`
	// Listen — the listen address.
	Listen string `json:"listen"`
	// UptimeSeconds — how long the daemon has been running.
	UptimeSeconds int64 `json:"uptime_seconds"`
	// Providers — the providers with their model and key counts.
	Providers []ProviderStatus `json:"providers"`
	// Addresses — the observed egress addresses by family. Empty when the daemon
	// could not learn them, which is a normal state rather than a failure: the
	// counters are then kept without an address.
	Addresses []egress.Address `json:"addresses,omitempty"`
	// Totals — the aggregate counters for today.
	Totals state.DaySnapshot `json:"totals_today"`
}

// EgressStatus is what the control surface needs from the address observer.
//
// An interface rather than the concrete resolver: the control surface reads one
// list, and the package that produced it is the entry point's business. It also
// keeps the surface testable without a network round trip.
type EgressStatus interface {
	Addresses() []egress.Address
}

// ProviderStatus — the state of a single provider.
type ProviderStatus struct {
	// ID — the identifier.
	ID string `json:"id"`
	// Models — how many models are declared.
	Models int `json:"models"`
	// FreeModels — how many of them are free.
	FreeModels int `json:"free_models"`
	// Keys — how many keys the provider has on file. Zero means none are
	// configured.
	//
	// The count is not the same as "in use": under the anonymous credential a
	// configured pool is deliberately not presented, which is what KeyUsed
	// reports.
	Keys int `json:"keys"`
	// KeyRotation — whether a refused key is replaced. False under the anonymous
	// credential, whatever the switch says, because there is nothing to replace.
	KeyRotation bool `json:"key_rotation"`
	// Credential — which credential is in force: "anonymous" or "account".
	Credential string `json:"credential,omitempty"`
	// KeyUsed — whether requests to this provider carry a key.
	KeyUsed bool `json:"key_used"`
}

// handleStatus answers with a snapshot of the state.
func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "status")
		return
	}

	st := Status{}
	if h.Config != nil {
		st.Mode = h.Config.Mode.Val
		st.Listen = h.Config.Listen.Val
	}
	if h.State != nil {
		snap := h.State.Snapshot()
		if len(snap.Days) > 0 {
			st.Totals = snap.Days[0]
		}
	}

	if h.Egress != nil {
		st.Addresses = h.Egress.Addresses()
	}

	if h.Providers != nil {
		for _, p := range h.Providers.All() {
			ps := ProviderStatus{ID: string(p.ID())}
			if models, err := p.Models(r.Context()); err == nil {
				ps.Models = len(models)
				for _, m := range models {
					if m.Free {
						ps.FreeModels++
					}
				}
			}
			if h.KeyRotation != nil {
				keys := h.KeyRotation.Keys(p.ID())
				ps.Keys = len(keys)
			}
			// The axes are reported from the configuration, which is what the
			// request path follows; the mechanism's own switch is not evidence
			// of what happens on the wire.
			//
			// An unknown credential (no configuration) falls back to the pool's
			// presence, which is the only thing left to go on.
			credential, rotates := h.credentialOf(p.ID())
			ps.Credential = credential
			ps.KeyRotation = rotates
			ps.KeyUsed = ps.Keys > 0 && (rotates || credential != config.CredentialAnonymous)
			st.Providers = append(st.Providers, ps)
		}
	}

	writeJSON(w, http.StatusOK, st)
}

// handleStats answers with the counters by day.
func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "stats")
		return
	}
	if h.State == nil {
		writeJSON(w, http.StatusOK, state.Snapshot{})
		return
	}
	writeJSON(w, http.StatusOK, h.State.Snapshot())
}

// handleModels answers with the catalog of all providers.
func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "models")
		return
	}

	type entry struct {
		Provider  string `json:"provider"`
		ID        string `json:"id"`
		Name      string `json:"name"`
		Context   int    `json:"context"`
		MaxOutput int    `json:"max_output"`
		Vision    bool   `json:"vision"`
		Reasoning bool   `json:"reasoning"`
		Free      bool   `json:"free"`
	}

	var out []entry
	if h.Providers != nil {
		for _, p := range h.Providers.All() {
			models, err := p.Models(r.Context())
			if err != nil {
				continue
			}
			for _, m := range models {
				out = append(out, entry{
					Provider: string(p.ID()), ID: m.ID, Name: m.Name,
					Context: m.Context, MaxOutput: m.MaxOutput,
					Vision: m.Vision, Reasoning: m.Reasoning, Free: m.Free,
				})
			}
		}
	}
	if out == nil {
		out = []entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": out})
}

// KeysResponse — the list of keys by provider.
//
// FINGERPRINTS are handed out, not the keys themselves: the help text is needed
// to understand how many keys are in use, not to see a secret.
type KeysResponse struct {
	Providers []ProviderKeys `json:"providers"`
}

// ProviderKeys — the keys of a single provider.
type ProviderKeys struct {
	Provider string   `json:"provider"`
	Keys     []string `json:"keys"`
	// Rotation — whether a refused key is replaced.
	//
	// FALSE UNDER THE ANONYMOUS CREDENTIAL, whatever the switch says: no key is
	// presented there, so there is nothing to replace. Reporting the switch
	// alone would let the monitor claim a mechanism that cannot act, which is
	// worse than saying nothing — the operator would then look for the reason a
	// rotation never happens in the wrong place.
	Rotation bool `json:"rotation"`
	// Credential — which credential is in force for this provider.
	Credential string `json:"credential"`
	// Used — whether requests to this provider carry a key.
	//
	// A pool can be configured and deliberately unsent, so this is a different
	// question from whether the key list is empty.
	Used bool `json:"key_used"`
}

// handleKeys answers with the list of keys.
func (h *Handler) handleKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "keys")
		return
	}
	writeJSON(w, http.StatusOK, h.keysResponse())
}

// keysResponse assembles the response with the list of keys.
func (h *Handler) keysResponse() KeysResponse {
	var resp KeysResponse
	if h.Providers == nil || h.KeyRotation == nil {
		resp.Providers = []ProviderKeys{}
		return resp
	}
	for _, id := range h.Providers.IDs() {
		credential, rotates := h.credentialOf(id)
		resp.Providers = append(resp.Providers, ProviderKeys{
			Provider:   string(id),
			Keys:       h.KeyRotation.KeyFingerprints(id),
			Rotation:   rotates,
			Credential: credential,
			Used:       rotates || (credential == config.CredentialAccount && h.KeyRotation.HasRotation(id)),
		})
	}
	if resp.Providers == nil {
		resp.Providers = []ProviderKeys{}
	}
	return resp
}

// credentialOf reports the credential in force and whether rotation can act.
//
// The answer comes from the configuration rather than from the mechanism's own
// switch, because the two disagree under the anonymous credential: the mechanism
// keeps its setting while the mode makes it unusable.
func (h *Handler) credentialOf(id provider.ID) (string, bool) {
	if h.Config == nil {
		// Without a configuration the axis is unknown, and the mechanism's own
		// switch is the only evidence there is.
		return "", h.KeyRotation.HasRotation(id)
	}
	credential := h.Config.CredentialFor(string(id))
	return credential, h.Config.RotatesFor(string(id))
}

// keyRequest — the body of a request to add or remove a key.
type keyRequest struct {
	// Provider — which provider the key belongs to.
	Provider string `json:"provider"`
	// Key — the key itself (when adding) or its fingerprint (when removing).
	Key string `json:"key"`
}

// handleKeyAdd adds a key.
//
// The key is accepted in the body rather than in the address: an address would
// end up in access logs and in the shell history.
func (h *Handler) handleKeyAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "keys/add")
		return
	}
	var req keyRequest
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "the body does not parse: " + err.Error()})
		return
	}
	if req.Provider == "" || req.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "the provider and key fields are required"})
		return
	}
	if h.KeyRotation == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "the key-rotation mechanism is not wired up"})
		return
	}

	added := h.KeyRotation.AddKey(provider.ID(req.Provider), req.Key)
	h.info("key added for %q (new: %t)", req.Provider, added)
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "keys": h.keysResponse()})
}

// handleKeyRemove removes a key by fingerprint.
//
// A fingerprint, not the key itself: the remover need not know the secret, it
// is enough for them to have what the help text showed.
func (h *Handler) handleKeyRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "keys/remove")
		return
	}
	var req keyRequest
	if err := decodeBody(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "the body does not parse: " + err.Error()})
		return
	}
	if req.Provider == "" || req.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "the provider and key fields are required"})
		return
	}
	if h.KeyRotation == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "the key-rotation mechanism is not wired up"})
		return
	}

	removed := h.KeyRotation.RemoveFingerprint(provider.ID(req.Provider), req.Key)
	h.info("key removed for %q (found: %t)", req.Provider, removed)
	writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "keys": h.keysResponse()})
}

// OptionsResponse — what can be chosen in the interfaces.
//
// Handed out by the daemon rather than stored in the CLI and the TUI: the list
// of values lives in one place, and it cannot be forgotten when updating. A
// copy in the interface goes stale silently — which is exactly what used to
// happen.
type OptionsResponse struct {
	// Providers — the provider identifiers.
	Providers []string `json:"providers"`
	// Modes — the deployment shapes.
	Modes []string `json:"modes"`
	// Transports — the available transports.
	Transports []string `json:"transports"`
	// Toggle — the on/off dictionary.
	Toggle []string `json:"toggle"`
	// Scopes — the data-clearing scopes.
	Scopes []string `json:"scopes"`
}

// handleOptions answers with the dictionaries of values.
func (h *Handler) handleOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "options")
		return
	}

	resp := OptionsResponse{
		Modes:  []string{"local", "server"},
		Toggle: []string{"on", "off"},
		Scopes: []string{"logs", "stats", "all"},
	}
	if h.Providers != nil {
		for _, id := range h.Providers.IDs() {
			resp.Providers = append(resp.Providers, string(id))
		}
	}
	if resp.Providers == nil {
		resp.Providers = []string{}
	}
	if h.Transports != nil {
		resp.Transports = h.Transports
	} else {
		resp.Transports = []string{}
	}
	writeJSON(w, http.StatusOK, resp)
}

// methodNotAllowed answers a wrong method.
func methodNotAllowed(w http.ResponseWriter, route string) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
		"error": "method not supported for " + route,
	})
}

// info writes a message to the journal, if there is one.
func (h *Handler) info(format string, args ...any) {
	if h.Log != nil {
		h.Log.Info(format, args...)
	}
}

// sortedKeys returns the keys of a map in a stable order.
//
// Needed for diagnostic output: without it the order would change between calls
// and two snapshots could not be compared.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
