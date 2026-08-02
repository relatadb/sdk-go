package relata

// Version is the Relata Go SDK's canonical release version. It tracks
// Cargo.toml's [workspace.package].version — the single source of truth for
// every companion surface (four SDKs, tray app, Grafana plugin, Helm chart;
// see CLAUDE.md "Key invariants"). scripts/check_versions.py verifies this
// constant against the canonical version on every CI run and rewrites it via
// --fix on a release bump; do not hand-edit it independently of that script.
// It is also the source for the SDK's User-Agent header (client.go).
const Version = "2.0.0"
