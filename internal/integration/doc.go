// Package integration holds end-to-end tests that drive the daemon against a
// real Kubernetes cluster (kind). The tests are guarded by the `integration`
// build tag so the normal `go test ./...` run skips them.
//
// To run locally:
//
//	mise run integration-up
//	mise run integration-test
//	mise run integration-down
package integration
