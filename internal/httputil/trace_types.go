package httputil

// ProxyTraceResponse is the recursive structure returned by /trace.
// Each router returns its own credential health plus the trace of each upstream proxy router.
type ProxyTraceResponse struct {
	RouterID    string                           `json:"router_id"`
	Status      string                           `json:"status"`
	Credentials map[string]CredentialHealthStats `json:"credentials"`
	Models      map[string]ModelHealthStats      `json:"models,omitempty"`
	Upstreams   map[string]*ProxyTraceResponse   `json:"upstreams,omitempty"` // proxy cred name → upstream trace
	FetchError  string                           `json:"fetch_error,omitempty"`
}
