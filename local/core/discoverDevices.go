package core

import (
	"context"
	"fmt"

	"github.com/go-ocf/go-coap/v2/message"
	"github.com/go-ocf/go-coap/v2/message/codes"
	"github.com/go-ocf/go-coap/v2/udp/client"
	"github.com/go-ocf/go-coap/v2/udp/message/pool"

	"github.com/go-ocf/kit/codec/ocf"
	"github.com/go-ocf/kit/net"
	"github.com/go-ocf/kit/net/coap"
	"github.com/go-ocf/sdk/schema"
)

// DiscoverDevicesHandler receives device links and errors from the discovery multicast request.
type DiscoverDevicesHandler interface {
	Handle(ctx context.Context, client *client.ClientConn, device schema.ResourceLinks)
	Error(err error)
}

// DiscoverDevices discovers devices using a CoAP multicast request via UDP.
// It waits for device responses until the context is canceled.
// Device resources can be queried in DiscoveryHandler.
// An empty typeFilter queries all resource types.
// Note: Iotivity 1.3 which responds with BadRequest if more than 1 resource type is queried.
func DiscoverDevices(
	ctx context.Context,
	conn []*DiscoveryClient,
	handler DiscoverDevicesHandler,
	options ...coap.OptionFunc,
) error {
	options = append(options, coap.WithAccept(message.AppOcfCbor))
	return Discover(ctx, conn, "/oic/res", handleResponse(ctx, handler), options...)
}

func handleResponse(ctx context.Context, handler DiscoverDevicesHandler) func(*client.ClientConn, *pool.Message) {
	return func(cc *client.ClientConn, r *pool.Message) {
		req := pool.ConvertTo(r)
		if req.Code != codes.Content {
			handler.Error(fmt.Errorf("request failed: %s", ocf.Dump(req)))
			return
		}

		var links schema.ResourceLinks
		var codec DiscoverDeviceCodec

		err := codec.Decode(req, &links)
		if err != nil {
			handler.Error(fmt.Errorf("decoding %v failed: %w", ocf.DumpHeader(req), err))
			return
		}
		addr, err := net.Parse(string(schema.UDPScheme), cc.RemoteAddr())
		if err != nil {
			handler.Error(fmt.Errorf("invalid address %v: %w", cc.RemoteAddr(), err))
			return
		}
		links = links.PatchEndpoint(addr)
		if len(links) > 0 {
			handler.Handle(ctx, cc, links)
		}
	}
}
