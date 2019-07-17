package local

import (
	"context"
	"fmt"
	"sync"

	gocoap "github.com/go-ocf/go-coap"
	"github.com/go-ocf/kit/codec/ocf"
	kitNetCoap "github.com/go-ocf/kit/net/coap"
	"github.com/gofrs/uuid"
)

func (d *Device) ObserveResourceWithCodec(
	ctx context.Context,
	href string,
	codec kitNetCoap.Codec,
	handler ObservationHandler,
	options ...kitNetCoap.OptionFunc,
) (observationID string, _ error) {
	return d.observeResource(ctx, href, codec, handler, options...)
}

type ObservationHandler interface {
	Handle(ctx context.Context, body []byte)
	OnClose()
	Error(err error)
}

func (d *Device) ObserveResource(
	ctx context.Context,
	href string,
	handler ObservationHandler,
	options ...kitNetCoap.OptionFunc,
) (observationID string, _ error) {
	codec := ocf.VNDOCFCBORCodec{}
	return d.ObserveResourceWithCodec(ctx, href, codec, handler, options...)
}

func (d *Device) StopObservingResource(
	ctx context.Context,
	observationID string,
) error {
	v, ok := d.observations.Load(observationID)
	if !ok {
		return fmt.Errorf("unknown observation %s", observationID)
	}
	d.observations.Delete(observationID)
	o := v.(*observation)
	err := o.Stop(ctx)
	if err != nil {
		return fmt.Errorf("could not cancel observation %s: %v", observationID, err)
	}

	return nil
}

func (d *Device) stopObservations(ctx context.Context) error {
	obs := make([]string, 0, 12)
	d.observations.Range(func(key, value interface{}) bool {
		observationID := key.(string)
		obs = append(obs, observationID)
		return false
	})
	var errors []error
	for _, observationID := range obs {
		err := d.StopObservingResource(ctx, observationID)
		if err != nil {
			errors = append(errors, err)
		}
	}
	if len(errors) > 0 {
		return fmt.Errorf("%v", errors)
	}
	return nil
}

type observation struct {
	id      string
	handler ObservationHandler
	client  *kitNetCoap.ClientCloseHandler

	lock      sync.Mutex
	onCloseID int
	obs       *gocoap.Observation
}

func (o *observation) Set(onCloseID int, obs *gocoap.Observation) {
	o.lock.Lock()
	defer o.lock.Unlock()

	o.onCloseID = onCloseID
	o.obs = obs
}

func (o *observation) Get() (onCloseID int, obs *gocoap.Observation) {
	o.lock.Lock()
	defer o.lock.Unlock()

	return o.onCloseID, o.obs
}

func (o *observation) Stop(ctx context.Context) error {
	onCloseID, obs := o.Get()
	o.client.UnregisterCloseHandler(onCloseID)
	if obs != nil {
		err := obs.CancelWithContext(ctx)
		if err != nil {
			return fmt.Errorf("cannot cancel observation %s: %v", o.id, err)
		}
		return err
	}
	return nil
}

func (d *Device) observeResource(
	ctx context.Context, href string,
	codec kitNetCoap.Codec,
	handler ObservationHandler,
	options ...kitNetCoap.OptionFunc,
) (observationID string, _ error) {

	client, err := d.connect(ctx, href)

	if err != nil {
		return "", err
	}

	options = append(options, kitNetCoap.WithAccept(codec.ContentFormat()))

	id, err := uuid.NewV4()
	if err != nil {
		return "", fmt.Errorf("observation id generation failed: %v", err)
	}
	h := observationHandler{handler: handler}
	o := &observation{
		id:      id.String(),
		handler: handler,
		client:  client,
	}
	onCloseID := client.RegisterCloseHandler(func(err error) {
		o.handler.OnClose()
		obsCtx, cancel := context.WithCancel(context.Background())
		cancel()
		d.StopObservingResource(obsCtx, o.id)
	})

	obs, err := client.Observe(ctx, href, codec, &h, options...)
	if err != nil {
		client.UnregisterCloseHandler(o.onCloseID)
		return "", err
	}

	o.Set(onCloseID, obs)

	d.observations.Store(o.id, o)
	return o.id, nil
}

type observationHandler struct {
	handler ObservationHandler
}

func (h *observationHandler) Handle(ctx context.Context, client *gocoap.ClientConn, body kitNetCoap.DecodeFunc) {
	var b []byte
	if err := body(&b); err != nil {
		h.handler.Error(err)
		return
	}
	h.handler.Handle(ctx, b)
}

func (h *observationHandler) Error(err error) {
	h.handler.Error(err)
}
