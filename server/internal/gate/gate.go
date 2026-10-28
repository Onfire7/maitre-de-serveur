// Package gate limits what each kind of trusted key may call on the agent's
// mutual-TLS listener (decision 189, ADR 0008). A Trusted Device may call
// anything. The Recovery Phrase may only add a Trusted Device (decision
// 100). A Node is not an administrator: it may only make the calls Nodes
// make of each other, so a compromised Node cannot update, reconfigure or
// read the logs of its peers.
package gate

import (
	"net/http"

	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
)

// Roles is what the gate needs from the trust list.
type Roles interface {
	Node(fingerprint string) bool
	Limited(fingerprint string) bool
}

// PutRecord is the one procedure the Recovery Phrase may call.
const PutRecord = "/maitre.v1.NodeService/PutRecord"

// NodeCalls are the procedures a Node may call on a peer: liveness and
// status, the Fleet database's exchange, forwarding a Device-signed change
// to its owner (PutRecord checks the signature), and a file move between
// two Nodes (FilesService checks it stays within Spaces both share).
var NodeCalls = map[string]bool{
	"/maitre.v1.NodeService/GetNodeInfo":     true,
	"/maitre.v1.NodeService/GetNodeMetrics":  true,
	"/maitre.v1.NodeService/GetUpdateStatus": true,
	"/maitre.v1.NodeService/GetFleetStatus":  true,
	"/maitre.v1.NodeService/ListRecords":     true,
	"/maitre.v1.NodeService/PushRecords":     true,
	"/maitre.v1.NodeService/PushStatus":      true,
	PutRecord:                                true,
	"/maitre.v1.FilesService/Move":           true,
	"/maitre.v1.FilesService/ListJobs":       true,
	"/maitre.v1.FilesService/CancelJob":      true,
}

// Handler refuses, with 403, requests a caller's role does not allow.
// Requests naming a Service (svcHeader) and plain HTTP paths count as calls
// of their own, which Nodes and the Recovery Phrase may not make.
func Handler(roles Roles, svcHeader string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := peer.From(r.Context())
		if ok {
			call := r.URL.Path
			if r.Header.Get(svcHeader) != "" {
				call = ""
			}
			switch {
			case roles.Limited(p.Fingerprint) && call != PutRecord:
				http.Error(w, "the Recovery Phrase may only add a Trusted Device", http.StatusForbidden)
				return
			case roles.Node(p.Fingerprint) && !NodeCalls[call]:
				http.Error(w, "a Node may not make this call of another Node (decision 189)", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
