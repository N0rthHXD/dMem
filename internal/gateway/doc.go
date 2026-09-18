// Package gateway defines the protocol-independent contracts of the Memory
// Gateway. It contains only cross-cutting value types and error contracts that
// every gateway module shares.
//
// Dependency rules enforced by architecture tests:
//
//   - This package and its subpackages must not import net/http or any protocol
//     package.
//   - This package and its subpackages must not import internal/storage or
//     internal/jobs, and must not expose storage.* types in public contracts.
//   - Gateway modules depend on narrow, consumer-owned ports rather than
//     concrete services from other modules.
package gateway
