package serviceservice

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect/v2"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/internal/quadlet"
)

// CheckService renders a spec as an install would, names the options that
// would need grants, and refuses what Quadlet refuses, installing nothing.
func TestCheckService(t *testing.T) {
	s := New(nil, nil)
	ctx := context.Background()
	spec := &maitrev1.ServiceSpec{Name: "app", Image: "docker.io/library/nginx:1@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Quadlet: "[Container]\nImage={{ .Image }}\nAddDevice=/dev/dri\n"}
	res, err := s.CheckService(ctx, &maitrev1.CheckServiceRequest{Spec: spec})
	if quadlet.Generator() == "" {
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Code() != connect.CodeUnavailable {
			t.Fatalf("without Quadlet: %v", err)
		}
		t.Skip("no Quadlet generator installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	if res.GetUnits()["app.container"] == "" || len(res.GetProtected()) != 1 || res.GetProtected()[0].GetOption() != "AddDevice=/dev/dri" {
		t.Fatalf("check: %+v", res)
	}
	spec.Quadlet = "[Container]\nImage={{ .Image }}\nNotAQuadletKey=1\n"
	_, err = s.CheckService(ctx, &maitrev1.CheckServiceRequest{Spec: spec})
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeInvalidArgument {
		t.Fatalf("bad unit: %v", err)
	}
}
