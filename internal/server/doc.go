// Package server owns the production HTTP transport for the Memory Gateway
// REST+JSON handler.
//
// It binds a loopback TCP listener, applies fixed transport hardening
// (read/write/idle timeouts and a header size cap), optionally terminates TLS
// with ALPN-negotiated HTTP/2, and shuts down gracefully. It is transport only:
// it never owns domain rules, gateway calls, routing, or request decoding.
//
// The package depends solely on net/http and crypto/tls plus a caller-supplied
// http.Handler. It never imports storage, jobs, gateway implementations,
// providers, retrieval, or the runtime composition root, and it does not depend
// on the config package; the composition root translates configuration into
// Config.
package server
