---
status: accepted
---

# Admin access uses paired devices and a replicated trust list, not accounts or a CA

The only admin surface is Maître Desktop, a native app, so the control plane never needs a browser-facing login. Every Node and every Maître Desktop install has its own key pair. The Fleet database holds the list of trusted public keys; pairing adds a key, signed by an existing Trusted Device or confirmed by a code on a Node's screen. The API accepts only mutual TLS from keys on that list. There are no usernames, passwords, or passkeys for admin access, and no certificate authority.

## Considered options

- **Username and password with TOTP**: phishable, something to remember, and forces a web-style login into a native app.
- **Passkeys**: strong, but migrating them between devices, especially from a dedicated app, is painful. Device pairing gives the same phishing resistance with "approve the new laptop" as the migration story.
- **Fleet CA signing device certificates**: a single private key whose loss or theft breaks or compromises the whole Fleet. A replicated list of keys, as in SSH and Tailscale, has no such secret.

## Consequences

A web UI cannot authenticate without reintroducing passwords, so there is none. Recovery when every Trusted Device is lost is the Recovery Phrase, or Recovery boot mode selected at the physical keyboard. All TLS uses the Go standard library; no custom protocol.
