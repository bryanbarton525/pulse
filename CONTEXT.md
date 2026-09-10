# Pulse terminology

- **Operational API:** authenticated component-to-component endpoints on port
  9091. These expose probe results, incidents, observations, and topology.
- **Metrics endpoint:** unauthenticated Prometheus and liveness endpoints on
  port 9090. NetworkPolicy limits who can reach it.
- **Internal token:** the bearer credential in
  `pulse-probe-auth.data.internal-token`; `auth.yaml` retains its reserved copy
  for backwards-compatible runtime configuration.
- **Embedding-space identity:** a SHA-256 fingerprint of a cold model's
  non-secret resolved configuration. Vectors with different identities are not
  comparable.
- **Inferred dependency:** a learned topology proposal. It is distinct from a
  declared dependency and expires when no longer supported by observations.
- **Declared dependency:** an operator-specified relationship used directly for
  correlation.
- **Last signal time:** the newest signal for the current incident, projected
  to canary status immediately for material changes and otherwise no more than
  once per minute of signal time.
