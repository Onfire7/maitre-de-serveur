---
status: accepted
---

# Pairing confirms a numeric comparison code; the Recovery Phrase is a derived key

Pairing a Console with an Installer (and later a new Trusted Device with a Node) shows a six-digit code on both screens, computed as SHA-256 over both parties' public keys. The person confirms the two codes match. The Recovery Phrase is twelve BIP39 English words encoding 128 bits of entropy; a domain-separated SHA-256 of that entropy seeds an Ed25519 key with the standard library. Nodes store only the public half, flagged recovery-only, so the phrase can approve a new Trusted Device and nothing else. Nothing secret is stored on any Node. (Design questions 66 and 67.)

## Considered options

- **Typed random code**: the Installer shows a code the person types into the Console. Works, but it only authenticates the Console to the Installer; a machine in the middle presenting its own key to the Console is not caught. Numeric comparison binds both keys at once and is one tap.
- **Password-derived recovery**: hash the phrase and store the hash on Nodes. Puts a brute-forceable secret on every Node. A derived public key reveals nothing about the phrase.
- **Recovery key with full admin rights**: simpler, but then a leaked phrase is a leaked Fleet. Recovery-only limits the blast radius to "can add a device", which the Console then shows in its event log.

## Consequences

The pairing code is computed identically in Go and Rust with a shared test vector. The Recovery Phrase path (approve a device from a new Console) lands in Milestone 3; Milestone 1 only writes the recovery key into every Fleet.
