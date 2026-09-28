package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"

	"github.com/tjorourke/flightbay/internal/collect"
)

// forwarder opens port-forwards the way kubectl does: WebSockets first,
// falling back to SPDY for older API servers. Listeners bind to 127.0.0.1 on
// ephemeral ports and close as soon as the pod's endpoints are read.
type forwarder struct {
	cfg *rest.Config
	cs  kubernetes.Interface
}

type forwardSession struct {
	stop   chan struct{}
	done   chan error
	local  map[int]int // remote port -> local port
	client *http.Client
}

func (f *forwarder) Open(ctx context.Context, ns, pod string, ports []int) (collect.ForwardSession, error) {
	u := f.cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(ns).Name(pod).SubResource("portforward").URL()
	rt, upgrader, err := spdy.RoundTripperFor(f.cfg)
	if err != nil {
		return nil, err
	}
	var dialer httpstream.Dialer = spdy.NewDialer(upgrader, &http.Client{Transport: rt}, http.MethodPost, u)
	if ws, err := portforward.NewSPDYOverWebsocketDialer(u, f.cfg); err == nil {
		dialer = portforward.NewFallbackDialer(ws, dialer, func(err error) bool {
			return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
		})
	}
	specs := make([]string, len(ports))
	for i, p := range ports {
		specs[i] = fmt.Sprintf("0:%d", p)
	}
	stop, ready := make(chan struct{}), make(chan struct{})
	pf, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, specs, stop, ready, io.Discard, io.Discard)
	if err != nil {
		return nil, err
	}
	s := &forwardSession{stop: stop, done: make(chan error, 1), local: map[int]int{}, client: &http.Client{Timeout: 30 * time.Second}}
	go func() { s.done <- pf.ForwardPorts() }()
	select {
	case <-ready:
	case err := <-s.done:
		return nil, fmt.Errorf("port-forward to %s/%s: %w", ns, pod, err)
	case <-ctx.Done():
		close(stop)
		return nil, ctx.Err()
	case <-time.After(20 * time.Second):
		close(stop)
		return nil, fmt.Errorf("port-forward to %s/%s: timed out", ns, pod)
	}
	got, err := pf.GetPorts()
	if err != nil {
		close(stop)
		return nil, err
	}
	for _, p := range got {
		s.local[int(p.Remote)] = int(p.Local)
	}
	return s, nil
}

func (s *forwardSession) Get(ctx context.Context, port int, path string, max int64) ([]byte, bool, error) {
	lp, ok := s.local[port]
	if !ok {
		return nil, false, fmt.Errorf("port %d is not forwarded", port)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", lp, path), nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return nil, false, fmt.Errorf("HTTP %d from port %d%s: %s", resp.StatusCode, port, path, string(b))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(b)) > max {
		return b[:max], true, nil
	}
	return b, false, nil
}

func (s *forwardSession) Close() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
}

// LocalPort is the 127.0.0.1 port a remote port is forwarded to (0 if not
// forwarded), for clients that speak something other than HTTP GET, such as
// the hubble CLI talking gRPC to hubble-relay.
func (s *forwardSession) LocalPort(remote int) int { return s.local[remote] }
