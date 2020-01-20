package local_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	ocf "github.com/go-ocf/sdk/local"
	"github.com/go-ocf/sdk/schema"

	"github.com/stretchr/testify/require"
)

func TestDeviceDiscovery(t *testing.T) {
	c := ocf.NewClient()
	timeout, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	h := testDeviceHandler{}
	err := c.GetDevices(timeout, &h)
	require.NoError(t, err)
}

type testDeviceHandler struct {
}

func (h *testDeviceHandler) Handle(ctx context.Context, d *ocf.Device, links schema.ResourceLinks) {
	defer d.Close(ctx)

	fmt.Printf("Device %v: %+v\n", d.DeviceID(), links)
	/*
		for _, t := range d.DeviceTypes() {
			if t == "oic.d.cloudDevice" {

				return
			}
		}
	*/
}

func (h *testDeviceHandler) Error(err error) {
	fmt.Printf("testDeviceHandler.Error: %v\n", err)
}
