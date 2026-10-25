package pool

import "time"

// Node is one server of the pass.
//
// Raw is the core's own profile, verbatim: rendering a download config is the
// core's business, and a lossy intermediate representation would eventually lose a
// field the core needs (xhttp, packet-encoding, reality spellings). The parsed
// fields exist for ranking, dialling and display only.
type Node struct {
	ID        string        `json:"id"`
	Proto     string        `json:"proto"`
	Transport string        `json:"transport,omitempty"`
	Security  string        `json:"security,omitempty"`
	Host      string        `json:"host"`
	Port      int           `json:"port"`
	SNI       string        `json:"sni,omitempty"`
	Label     string        `json:"label,omitempty"`
	Raw       string        `json:"raw"`
	ProbeRTT  time.Duration `json:"probe_rtt,omitempty"`
	ExitIP    string        `json:"exit_ip,omitempty"`
	ExitGeo   string        `json:"exit_geo,omitempty"`
}
