// Package core defines the protocol types and interfaces shared across all
// Oasis subpackages and satellites. It is a leaf package: nothing under
// github.com/nevindra/oasis/* imports anything other than core itself.
//
// Most consumers should use github.com/nevindra/oasis, which re-exports the
// common surface in a single curated package. Power users and satellite
// authors import core directly for protocol types and interfaces — that's
// supported, but the API surface is larger and evolves with breaking minor
// bumps.
package core
