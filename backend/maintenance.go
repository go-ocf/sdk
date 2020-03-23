package backend

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-ocf/grpc-gateway/pb"
	"github.com/go-ocf/sdk/schema/maintenance"
)

// Reboot makes reboot on device. JWT token must be stored in context for grpc call.
func (c *Client) Reboot(
	ctx context.Context,
	deviceID string,
) error {
	return c.updateMaintenanceResource(ctx, deviceID, maintenance.MaintenanceUpdateRequest{
		Reboot: true,
	})
}

// FactoryReset makes factory reset on device. JWT token must be stored in context for grpc call.
func (c *Client) FactoryReset(
	ctx context.Context,
	deviceID string,
) error {
	return c.updateMaintenanceResource(ctx, deviceID, maintenance.MaintenanceUpdateRequest{
		FactoryReset: true,
	})
}

func (c *Client) updateMaintenanceResource(
	ctx context.Context,
	deviceID string,
	req maintenance.MaintenanceUpdateRequest,
) (ret error) {
	it := c.GetResourceLinksIterator(ctx, []string{deviceID}, maintenance.MaintenanceResourceType)
	defer it.Close()
	for {
		var v pb.ResourceLink
		if !it.Next(&v) {
			break
		}
		var resp maintenance.Maintenance
		err := c.UpdateResource(ctx, v.GetDeviceId(), v.GetHref(), req, &resp)
		if err != nil {
			return err
		}
		if resp.LastHTTPError >= http.StatusBadRequest {
			str := http.StatusText(resp.LastHTTPError)
			defer func() {
				if r := recover(); r != nil {
					ret = fmt.Errorf("returns HTTP code %v", resp.LastHTTPError)
				}
			}()
			return fmt.Errorf(str)
		}
		return it.Err
	}
	return it.Err
}
