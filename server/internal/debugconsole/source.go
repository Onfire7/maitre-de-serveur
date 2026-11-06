// Package debugconsole is the Debug Console (decision 323): the separate
// app that diagnoses a Node, paired as a device is.
//
// It is one surface in two shapes. Every page of the screen is also a
// command — `maitre debug space` is the Space page, `maitre debug read` is
// the Files page — because the thing that ends an incident is often a
// script run over four Nodes at once, and a console that can only be
// watched cannot be scripted. Both shapes go through the Source below, so
// there is one definition of what the Console can ask and no page that
// only the screen can reach.
//
// What it asks for spans decision 326's first two tiers, and it says which
// is which. The basic tier — the gauges, the image list, a unit's journal,
// prune — is in every shipped image, for a Trusted Device that can already
// install Services. The debug tier — reading a file, running a command —
// answers only from an image built with the `maitredebug` tag, so against
// a shipped Node those two pages say so plainly rather than looking
// broken. The third tier, the rescue shell, is deliberately out of reach
// from here: it exists only where networking is off.
//
// The Console pairs as a Trusted Device today. `authz.Role` has no fourth
// `Debug` member yet, so a Node that trusts this app trusts it as fully as
// it trusts Maître Desktop; the debug tier refuses a Node caller rather
// than demanding a role of its own.
package debugconsole

import (
	"context"
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	maitrev1 "github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1"
	"github.com/Onfire7/maitre-de-serveur/server/gen/maitre/v1/maitrev1connect"
)

// Source is everything the Console can ask a Node. The screen and the
// commands both go through it, and the tests fake it.
type Source interface {
	// Info, UpdateStatus and Metrics are the Overview page: who this Node
	// is, what image it runs, and how much room it has left.
	Info(ctx context.Context) (*maitrev1.NodeInfo, error)
	UpdateStatus(ctx context.Context) (*maitrev1.UpdateStatus, error)
	Metrics(ctx context.Context) (*maitrev1.NodeMetrics, error)
	// Images, Prune and MoveStore are the Space page (decisions 316, 319,
	// 320, 326).
	Images(ctx context.Context) (*maitrev1.ListImagesResponse, error)
	Prune(ctx context.Context, keepRollback bool) (*maitrev1.PruneImagesResponse, error)
	MoveStore(ctx context.Context, dir string) (*maitrev1.MoveImageStoreResponse, error)
	// Services and Logs are the basic tier's other half.
	Services(ctx context.Context) ([]*maitrev1.ServiceStatus, error)
	Logs(ctx context.Context, source string, lines int) (*maitrev1.GetNodeLogsResponse, error)
	// ReadFile and Exec are the debug tier, and answer only from an image
	// built with the debug tag.
	ReadFile(ctx context.Context, path string) ([]byte, error)
	Exec(ctx context.Context, command string, args []string) (string, error)
}

// Client is a Source backed by a Node over mutual TLS.
type Client struct {
	node maitrev1connect.NodeServiceClient
	svc  maitrev1connect.ServiceServiceClient
}

// Dial points a Client at addr. The caller builds the HTTP client from its
// own identity and trust list, as every other command does.
func Dial(hc *http.Client, addr string) *Client {
	transport := connecthttp.NewTransport(hc, "https://"+addr)
	return &Client{
		node: maitrev1connect.NewNodeServiceClient(connect.NewClient(transport)),
		svc:  maitrev1connect.NewServiceServiceClient(connect.NewClient(transport)),
	}
}

func (c *Client) Info(ctx context.Context) (*maitrev1.NodeInfo, error) {
	res, err := c.node.GetNodeInfo(ctx, &maitrev1.GetNodeInfoRequest{})
	return res.GetNode(), err
}

func (c *Client) UpdateStatus(ctx context.Context) (*maitrev1.UpdateStatus, error) {
	res, err := c.node.GetUpdateStatus(ctx, &maitrev1.GetUpdateStatusRequest{})
	return res.GetStatus(), err
}

func (c *Client) Metrics(ctx context.Context) (*maitrev1.NodeMetrics, error) {
	res, err := c.node.GetNodeMetrics(ctx, &maitrev1.GetNodeMetricsRequest{})
	return res.GetMetrics(), err
}

func (c *Client) Images(ctx context.Context) (*maitrev1.ListImagesResponse, error) {
	res, err := c.svc.ListImages(ctx, &maitrev1.ListImagesRequest{})
	return res, err
}

func (c *Client) Prune(ctx context.Context, keepRollback bool) (*maitrev1.PruneImagesResponse, error) {
	res, err := c.svc.PruneImages(ctx, &maitrev1.PruneImagesRequest{KeepRollback: keepRollback})
	return res, err
}

func (c *Client) MoveStore(ctx context.Context, dir string) (*maitrev1.MoveImageStoreResponse, error) {
	res, err := c.svc.MoveImageStore(ctx, &maitrev1.MoveImageStoreRequest{Dir: dir})
	return res, err
}

func (c *Client) Services(ctx context.Context) ([]*maitrev1.ServiceStatus, error) {
	res, err := c.svc.ListServices(ctx, &maitrev1.ListServicesRequest{})
	return res.GetServices(), err
}

func (c *Client) Logs(ctx context.Context, source string, lines int) (*maitrev1.GetNodeLogsResponse, error) {
	res, err := c.node.GetNodeLogs(ctx, &maitrev1.GetNodeLogsRequest{Source: source, Lines: uint32(lines)})
	return res, err
}

func (c *Client) ReadFile(ctx context.Context, path string) ([]byte, error) {
	res, err := c.node.DebugReadFile(ctx, &maitrev1.DebugReadFileRequest{Path: path})
	return res.GetContent(), err
}

func (c *Client) Exec(ctx context.Context, command string, args []string) (string, error) {
	res, err := c.node.DebugExec(ctx, &maitrev1.DebugExecRequest{Command: command, Args: args})
	// The output is the diagnosis even when the command failed, so it
	// comes back either way.
	return res.GetOutput(), err
}

// TooOld reports whether err is a Node that does not have the call at all.
// The newer RPCs must stay optional, so the Console says "this Node is
// older than this" rather than showing a broken page.
func TooOld(err error) bool { return connect.CodeOf(err) == connect.CodeUnimplemented }

// Suggestions are the paths worth reading first when a Node is being
// diagnosed, offered as completions on the Files page. A convenience and
// not an authority: the Node's own allowlist decides what it will read,
// and its refusal names the directories it allows.
var Suggestions = []string{
	"/etc/containers/storage.conf",
	"/etc/containers/containers.conf",
	"/etc/containers/containers.conf.d/maitre-image-store.conf",
	"/etc/greenboot/check/required.d/10-maitre.sh",
	"/etc/greenboot/check/wanted.d/20-maitre-space.sh",
	"/etc/greenboot/red.d/10-maitre.sh",
	"/proc/mounts",
	"/proc/meminfo",
	"/proc/net/dev",
	"/var/lib/maitre/red-boots",
	"/var/lib/maitre/last-rollback",
}

// Commands are the commands worth running first, offered as completions on
// the Run page. Diagnosis before repair: each of these answers a question
// the pages cannot.
var Commands = []string{
	"bootc status",
	"df -h",
	"podman images",
	"systemctl status maitre-agent",
	"systemctl list-units --failed",
	"journalctl -u maitre-agent -n 50 --no-pager",
	"zramctl",
	"findmnt",
}
