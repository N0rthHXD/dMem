// Package httpapi implements the first Agent-neutral REST+JSON protocol adapter
// for Memory Gateway.
//
// The package translates HTTP requests into gateway module calls. It owns route
// dispatch, strict representation decoding, body limits, request identifiers,
// caller and idempotency metadata extraction, gateway result serialization,
// gateway error mapping, and panic recovery at the protocol boundary. It does
// not own domain validation, memory lifecycle rules, retrieval planning,
// provider policy, transaction management, or repository access.
//
// Dependencies are consumer-owned interfaces over the gateway module contracts.
// The adapter never imports internal/storage, internal/jobs, or a concrete
// gateway service implementation. It returns an http.Handler and does not create
// a listener, an http.Server, or a TLS configuration; enforcing HTTP/2 is the
// responsibility of the future server layer.
//
// The wire contract is frozen in docs/api/http-rest-contract.md. Routes are
// declared as inspectable metadata so contract conformance can be tested without
// exercising every handler.
package httpapi
