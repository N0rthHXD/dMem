// Package mcp is the stdio Model Context Protocol adapter for the dMem Agent
// Tool Route.
//
// The adapter is a thin protocol translator: it decodes MCP JSON-RPC messages,
// maps the four dMem tools onto the public application client, and serializes
// results and structured errors. It never calls a model, never uses MCP
// sampling, never touches storage, jobs, or provider code, and never owns
// memory domain rules.
package mcp
