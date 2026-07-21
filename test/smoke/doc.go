// Package smoke holds integration smoke tests for the pgpilot development
// cluster defined in docker-compose.yml.
//
// The tests are guarded by the "integration" build tag and drive the live
// cluster through docker compose, so they are excluded from the default
// `go test ./...` run. Bring the cluster up first, then run them:
//
//	make up
//	make smoke
package smoke
