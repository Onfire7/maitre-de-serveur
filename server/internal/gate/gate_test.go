package gate_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Onfire7/maitre-de-serveur/server/internal/gate"
	"github.com/Onfire7/maitre-de-serveur/server/internal/peer"
)

type roles struct{ nodes, limited map[string]bool }

func (r roles) Node(fp string) bool    { return r.nodes[fp] }
func (r roles) Limited(fp string) bool { return r.limited[fp] }

func TestGate(t *testing.T) {
	h := gate.Handler(roles{nodes: map[string]bool{"node": true}, limited: map[string]bool{"phrase": true}}, "X-Maitre-Service",
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	for _, c := range []struct {
		who, path string
		svc       bool
		ok        bool
	}{
		{"device", "/maitre.v1.NodeService/UpdateImage", false, true},
		{"device", "/", true, true},
		{"node", "/maitre.v1.NodeService/GetNodeInfo", false, true},
		{"node", "/maitre.v1.NodeService/PushRecords", false, true},
		{"node", "/maitre.v1.NodeService/PutRecord", false, true},
		{"node", "/maitre.v1.FilesService/Move", false, true},
		{"node", "/maitre.v1.NodeService/UpdateImage", false, false},
		{"node", "/maitre.v1.NodeService/Reboot", false, false},
		{"node", "/maitre.v1.NodeService/GetNodeLogs", false, false},
		{"node", "/maitre.v1.ServiceService/InstallService", false, false},
		{"node", "/maitre.v1.FilesService/Delete", false, false},
		{"node", "/maitre/files/content", false, false},
		{"node", "/maitre.v1.NodeService/GetNodeInfo", true, false},
		{"phrase", "/maitre.v1.NodeService/PutRecord", false, true},
		{"phrase", "/maitre.v1.NodeService/ListRecords", false, false},
	} {
		r := httptest.NewRequest(http.MethodPost, c.path, nil)
		if c.svc {
			r.Header.Set("X-Maitre-Service", "web")
		}
		r = r.WithContext(peer.WithInfo(r.Context(), peer.Info{Fingerprint: c.who}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if (w.Code == http.StatusTeapot) != c.ok {
			t.Errorf("%s %s (service %v): %d", c.who, c.path, c.svc, w.Code)
		}
	}
}
