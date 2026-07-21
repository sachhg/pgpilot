// Package integration holds end-to-end tests that run the pgpilot proxy against
// the live docker-compose cluster.
//
// The tests are guarded by the "integration" build tag and are excluded from
// the default `go test ./...` run. Bring the cluster up first, then run them:
//
//	make up
//	make itest
package integration
