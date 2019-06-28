package local

import (
	"context"

	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"

	"sync"

	gocoap "github.com/go-ocf/go-coap"
	kitNet "github.com/go-ocf/kit/net"
	kitNetCoap "github.com/go-ocf/kit/net/coap"
	"github.com/go-ocf/sdk/local/resource"
	"github.com/go-ocf/sdk/schema"
	"github.com/go-ocf/sdk/schema/acl"
)

type deviceOwnershipClient struct {
	*coapClient
	ownership schema.Doxm
}

func (c *deviceOwnershipClient) GetOwnership() schema.Doxm {
	return c.ownership
}

func (c *deviceOwnershipClient) GetTcpSecureAddress(ctx context.Context, deviceID string) (string, error) {
	deviceLink, err := c.GetDeviceLinks(ctx, deviceID)
	if err != nil {
		return "", err
	}
	var resourceLink schema.ResourceLink
	for _, link := range deviceLink.Links {
		if link.HasType("oic.wk.d") {
			resourceLink = link
			break
		}
	}
	tcpTLSAddr, err := resourceLink.GetTCPSecureAddr()
	if err != nil {
		return "", err
	}
	return tcpTLSAddr.String(), nil
}

type deviceOwnershipHandler struct {
	deviceID string
	cancel   context.CancelFunc

	client *deviceOwnershipClient
	lock   sync.Mutex
	err    error
}

func newDeviceOwnershipHandler(deviceID string, cancel context.CancelFunc) *deviceOwnershipHandler {
	return &deviceOwnershipHandler{deviceID: deviceID, cancel: cancel}
}

func (h *deviceOwnershipHandler) Handle(ctx context.Context, clientConn *gocoap.ClientConn, ownership schema.Doxm) {
	h.lock.Lock()
	defer h.lock.Unlock()
	if ownership.DeviceId == h.deviceID && h.client == nil {
		h.client = &deviceOwnershipClient{coapClient: NewCoapClient(clientConn, schema.UDPScheme), ownership: ownership}
		h.cancel()
	}
}

func (h *deviceOwnershipHandler) Error(err error) {
	h.lock.Lock()
	defer h.lock.Unlock()
	if h.err == nil {
		h.err = err
	}
}

func (h *deviceOwnershipHandler) Err() error {
	h.lock.Lock()
	defer h.lock.Unlock()
	return h.err
}

func (h *deviceOwnershipHandler) Client() *deviceOwnershipClient {
	h.lock.Lock()
	defer h.lock.Unlock()
	return h.client
}

func (c *Client) ownDeviceFindClient(ctx context.Context, deviceID string, status resource.DiscoverOwnershipStatus) (*deviceOwnershipClient, error) {
	ctxOwn, cancel := context.WithCancel(ctx)
	defer cancel()
	h := newDeviceOwnershipHandler(deviceID, cancel)

	err := c.GetDeviceOwnership(ctxOwn, status, h)
	client := h.Client()

	if client != nil {
		return client, nil
	}
	if err != nil {
		return nil, err
	}
	err = h.Err()
	if err != nil {
		return nil, err
	}

	return nil, fmt.Errorf("device not found")
}

type OTMClient interface {
	Type() schema.OwnerTransferMethod
	Dial(ctx context.Context, addr kitNet.Addr) (*kitNetCoap.Client, error)
	ProvisionOwnerCredentials(ctx context.Context, client *kitNetCoap.Client, ownerID, deviceID string) error
}

type ManufacturerOTMClient struct {
	manufacturerCertificate tls.Certificate
	manufacturerCA          *x509.Certificate

	signer CertificateSigner
	CAs    []*x509.Certificate
}

func NewManufacturerOTMClient(manufacturerCertificate tls.Certificate, manufacturerCA *x509.Certificate, signer CertificateSigner, CAs []*x509.Certificate) *ManufacturerOTMClient {
	return &ManufacturerOTMClient{
		manufacturerCertificate: manufacturerCertificate,
		manufacturerCA:          manufacturerCA,
		signer:                  signer,
		CAs:                     CAs,
	}
}

func (*ManufacturerOTMClient) Type() schema.OwnerTransferMethod {
	return schema.ManufacturerCertificate
}

func (otmc *ManufacturerOTMClient) Dial(ctx context.Context, addr kitNet.Addr) (*kitNetCoap.Client, error) {
	switch addr.GetScheme() {
	case schema.TCPSecureScheme:
		return kitNetCoap.DialTcpTls(ctx, addr.String(), otmc.manufacturerCertificate, []*x509.Certificate{otmc.manufacturerCA}, func(*x509.Certificate) error { return nil })
	}
	return nil, fmt.Errorf("cannot dial to url %v: scheme %v not supported", addr.URL(), addr.GetScheme())
}

func encodeToDer(encoding schema.CertificateEncoding, data []byte) ([]byte, error) {
	der := data
	if encoding == schema.CertificateEncoding_PEM {
		derBlock, _ := pem.Decode(data)
		if derBlock == nil {
			return nil, fmt.Errorf("invalid pem encoding")
		}
		der = derBlock.Bytes
	}
	return der, nil
}

func (otmc *ManufacturerOTMClient) ProvisionOwnerCredentials(ctx context.Context, tlsClient *kitNetCoap.Client, ownerID, deviceID string) error {
	/*setup credentials - PostOwnerCredential*/
	var csr schema.CertificateSigningRequestResponse
	err := tlsClient.GetResource(ctx, "/oic/sec/csr", &csr)
	if err != nil {
		return fmt.Errorf("cannot get csr for setup device owner credentials: %v", err)
	}

	der, err := encodeToDer(csr.Encoding, csr.CertificateSigningRequest)
	if err != nil {
		return fmt.Errorf("cannot encode csr to der: %v", err)
	}

	signedCsr, err := otmc.signer.Sign(ctx, der)
	if err != nil {
		return fmt.Errorf("cannot sign csr for setup device owner credentials: %v", err)
	}

	var deviceCredential schema.CredentialResponse
	err = tlsClient.GetResource(ctx, "/oic/sec/cred", &deviceCredential, kitNetCoap.WithCredentialSubject(deviceID))
	if err != nil {
		return fmt.Errorf("cannot get device credential to setup device owner credentials: %v", err)
	}

	for _, cred := range deviceCredential.Credentials {
		switch {
		case cred.Usage == schema.CredentialUsage_CERT && cred.Type == schema.CredentialType_ASYMMETRIC_SIGNING_WITH_CERTIFICATE,
			cred.Usage == schema.CredentialUsage_TRUST_CA && cred.Type == schema.CredentialType_ASYMMETRIC_SIGNING_WITH_CERTIFICATE:
			err = tlsClient.DeleteResource(ctx, "/oic/sec/cred", nil, kitNetCoap.WithCredentialId(cred.ID))
			if err != nil {
				return fmt.Errorf("cannot delete device credentials %v (%v) to setup device owner credentials: %v", cred.ID, cred.Usage, err)
			}
		}
	}

	setIdentityDeviceCredential := schema.CredentialUpdateRequest{
		ResourceOwner: ownerID,
		Credentials: []schema.Credential{
			schema.Credential{
				Subject: deviceID,
				Type:    schema.CredentialType_ASYMMETRIC_SIGNING_WITH_CERTIFICATE,
				Usage:   schema.CredentialUsage_CERT,
				PublicData: schema.CredentialPublicData{
					Data:     string(signedCsr),
					Encoding: schema.CredentialPublicDataEncoding_DER,
				},
			},
		},
	}
	err = tlsClient.UpdateResource(ctx, "/oic/sec/cred", setIdentityDeviceCredential, nil)
	if err != nil {
		return fmt.Errorf("cannot set device identity credentials: %v", err)
	}

	for _, ca := range otmc.CAs {
		setCaCredential := schema.CredentialUpdateRequest{
			ResourceOwner: ownerID,
			Credentials: []schema.Credential{
				schema.Credential{
					Subject: ownerID,
					Type:    schema.CredentialType_ASYMMETRIC_SIGNING_WITH_CERTIFICATE,
					Usage:   schema.CredentialUsage_TRUST_CA,
					PublicData: schema.CredentialPublicData{
						Data:     string(ca.Raw),
						Encoding: schema.CredentialPublicDataEncoding_DER,
					},
				},
			},
		}
		err = tlsClient.UpdateResource(ctx, "/oic/sec/cred", setCaCredential, nil)
		if err != nil {
			return fmt.Errorf("cannot set device CA credentials: %v", err)
		}
	}
	return nil
}

func (otmc *ManufacturerOTMClient) SignCertificate(ctx context.Context, csr []byte) (signedCsr []byte, err error) {
	return otmc.signer.Sign(ctx, csr)
}

func encodeToPem(encoding schema.CertificateEncoding, data []byte) string {
	switch encoding {
	case schema.CertificateEncoding_DER:
		d := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: data})
		d = append(d, []byte{0}...)
		return string(d)
	}
	return string(data)
}

/*
 * iotivityHack sets credential with pair-wise and removes it. It's needed to
 * enable ciphers for TLS communication with signed certificates.
 */
func iotivityHack(ctx context.Context, tlsClient *kitNetCoap.Client, sdkID string) error {
	hackId := "52a201a7-824c-4fc6-9092-d2b6a3414a5b"

	setDeviceOwner := schema.DoxmUpdate{
		DeviceOwner: hackId,
	}

	/*doxm doesn't send any content for select OTM*/
	err := tlsClient.UpdateResource(ctx, "/oic/sec/doxm", setDeviceOwner, nil)
	if err != nil {
		return fmt.Errorf("cannot set device hackid as owner %v", err)
	}

	iotivityHackCredential := schema.CredentialUpdateRequest{
		ResourceOwner: sdkID,
		Credentials: []schema.Credential{
			schema.Credential{
				Subject: hackId,
				Type:    schema.CredentialType_SYMMETRIC_PAIR_WISE,
				PrivateData: schema.CredentialPrivateData{
					Data:     "IOTIVITY HACK",
					Encoding: schema.CredentialPrivateDataEncoding_RAW,
				},
			},
		},
	}
	err = tlsClient.UpdateResource(ctx, "/oic/sec/cred", iotivityHackCredential, nil)
	if err != nil {
		return fmt.Errorf("cannot set iotivity-hack credential: %v", err)
	}

	err = tlsClient.DeleteResource(ctx, "/oic/sec/cred", nil, kitNetCoap.WithCredentialSubject(hackId))
	if err != nil {
		return fmt.Errorf("cannot delete iotivity-hack credential: %v", err)
	}

	return nil
}

// OwnDevice set ownership of device
func (c *Client) OwnDevice(
	ctx context.Context,
	deviceID string,
	otmClient OTMClient,
) error {
	const errMsg = "cannot own device %v: %v"

	client, err := c.ownDeviceFindClient(ctx, deviceID, resource.DiscoverAllDevices)
	if err != nil {
		return fmt.Errorf(errMsg, deviceID, err)
	}
	defer client.Close()

	ownership := client.GetOwnership()
	var supportOtm bool
	for _, s := range ownership.SupportedOwnerTransferMethods {
		if s == otmClient.Type() {
			supportOtm = true
			break
		}
	}
	if !supportOtm {
		return fmt.Errorf(errMsg, deviceID, fmt.Sprintf("ownership transfer method '%v' is unsupported, supported are: %v", otmClient.Type(), ownership.SupportedOwnerTransferMethods))
	}

	selectOTM := schema.DoxmUpdate{
		SelectOwnerTransferMethod: otmClient.Type(),
	}

	sdkID, err := c.GetSdkDeviceID()
	if err != nil {
		return fmt.Errorf(errMsg, deviceID, fmt.Errorf("cannot set device owner %v", err))
	}

	/*doxm doesn't send any content for select OTM*/
	err = client.UpdateResource(ctx, "/oic/sec/doxm", selectOTM, nil)
	if err != nil {
		if ownership.Owned {
			if ownership.DeviceOwner == sdkID {
				return nil
			}
			return fmt.Errorf(errMsg, deviceID, fmt.Errorf("device is already owned by %v", ownership.DeviceOwner))
		}
		return fmt.Errorf(errMsg, deviceID, fmt.Errorf("cannot select OTM: %v", err))
	}

	deviceClient, err := c.GetDevice(ctx, deviceID, nil)
	if err != nil {
		return fmt.Errorf(errMsg, deviceID, err)
	}
	links := deviceClient.GetResourceLinks()
	if len(links) == 0 {
		return fmt.Errorf(errMsg, deviceID, "device links are empty")
	}
	var tlsAddr kitNet.Addr
	var tlsAddrFound bool
	for _, link := range deviceClient.GetResourceLinks() {
		if tlsAddr, err = link.GetTCPSecureAddr(); err == nil {
			tlsAddrFound = true
			break
		}
	}
	if !tlsAddrFound {
		return fmt.Errorf(errMsg, deviceID, fmt.Errorf("cannot get tcp secure address: not found"))
	}

	tlsClient, err := otmClient.Dial(ctx, tlsAddr)
	if err != nil {
		return fmt.Errorf(errMsg, deviceID, fmt.Errorf("cannot create TLS connection: %v", err))
	}

	var provisionState schema.ProvisionStatusResponse
	err = tlsClient.GetResource(ctx, "/oic/sec/pstat", &provisionState)
	if err != nil {
		return fmt.Errorf(errMsg, deviceID, fmt.Errorf("cannot get provision state %v", err))
	}

	if provisionState.DeviceOnboardingState.Pending {
		return fmt.Errorf(errMsg, deviceID, fmt.Errorf("device pending for operation state %v", provisionState.DeviceOnboardingState.CurrentOrPendingOperationalState))
	}

	if provisionState.DeviceOnboardingState.CurrentOrPendingOperationalState != schema.OperationalState_RFOTM {
		return fmt.Errorf(errMsg, deviceID, fmt.Errorf("device operation state %v is not %v", provisionState.DeviceOnboardingState.CurrentOrPendingOperationalState, schema.OperationalState_RFOTM))
	}

	if !provisionState.SupportedOperationalModes.Has(schema.OperationalMode_CLIENT_DIRECTED) {
		return fmt.Errorf(errMsg, deviceID, fmt.Errorf("device supports %v, but only %v is supported", provisionState.SupportedOperationalModes, schema.OperationalMode_CLIENT_DIRECTED))
	}

	updateProvisionState := schema.ProvisionStatusUpdateRequest{
		CurrentOperationalMode: schema.OperationalMode_CLIENT_DIRECTED,
	}
	/*pstat doesn't send any content for select OperationalMode*/
	err = tlsClient.UpdateResource(ctx, "/oic/sec/pstat", updateProvisionState, nil)
	if err != nil {
		return fmt.Errorf(errMsg, deviceID, fmt.Errorf("cannot update provision state %v", err))
	}

	/*setup credentials */
	err = otmClient.ProvisionOwnerCredentials(ctx, tlsClient, sdkID, deviceID)
	if err != nil {
		return fmt.Errorf(errMsg, deviceID, fmt.Errorf("cannot provision owner %v", err))
	}

	/*
	 * THIS IS HACK FOR iotivity -> enables ciphers for TLS communication with signed certificates.
	 * Tested with iotivity 2.0.1-RC0.
	 */
	isIotivity, err := IsIotivity(ctx, tlsClient)
	if err != nil {
		return fmt.Errorf(errMsg, deviceID, err)
	}
	if isIotivity {
		err = iotivityHack(ctx, tlsClient, sdkID)
		if err != nil {
			return fmt.Errorf(errMsg, deviceID, err)
		}
	}
	// END OF HACK

	setDeviceOwner := schema.DoxmUpdate{
		DeviceOwner: sdkID,
	}

	/*doxm doesn't send any content for select OTM*/
	err = tlsClient.UpdateResource(ctx, "/oic/sec/doxm", setDeviceOwner, nil)
	if err != nil {
		return fmt.Errorf(errMsg, deviceID, fmt.Errorf("cannot set device owner %v", err))
	}

	/*verify ownership*/
	var verifyOwner schema.Doxm
	err = tlsClient.GetResource(ctx, "/oic/sec/doxm", &verifyOwner)
	if err != nil {
		return fmt.Errorf(errMsg, deviceID, fmt.Errorf("cannot verify owner: %v", err))
	}
	if verifyOwner.DeviceOwner != sdkID {
		return fmt.Errorf(errMsg, deviceID, err)
	}

	setDeviceOwned := schema.DoxmUpdate{
		ResourceOwner: sdkID,
		DeviceId:      deviceID,
		Owned:         true,
	}

	/*doxm doesn't send any content for select OTM*/
	err = tlsClient.UpdateResource(ctx, "/oic/sec/doxm", setDeviceOwned, nil)
	if err != nil {
		return fmt.Errorf(errMsg, deviceID, fmt.Errorf("cannot set device owned %v", err))
	}

	//For Servers based on OCF 1.0, PostOwnerAcl can be executed using
	//the already-existing session. However, get ready here to use the
	//Owner Credential for establishing future secure sessions.
	//
	//For Servers based on OIC 1.1, PostOwnerAcl might fail with status
	//OC_STACK_UNAUTHORIZED_REQ. After such a failure, OwnerAclHandler
	//will close the current session and re-establish a new session,
	//using the Owner Credential.

	tlsClient.Close()

	setOwnerProvisionState := schema.ProvisionStatusUpdateRequest{
		ResourceOwner: sdkID,
	}

	/*pstat set owner of resource*/
	err = c.UpdateResource(ctx, deviceID, "/oic/sec/pstat", setOwnerProvisionState, nil)
	if err != nil {
		return fmt.Errorf(errMsg, deviceID, fmt.Errorf("cannot update provision state resource owner to setup device owner ACLs: %v", err))
	}

	/*acl2 set owner of resource*/
	setOwnerAcl := acl.UpdateRequest{
		ResourceOwner: sdkID,
		AccessControlList: []acl.AccessControl{
			acl.AccessControl{
				Permission: acl.AllPermissions,
				Subject: acl.Subject{
					Subject_Device: &acl.Subject_Device{
						DeviceId: sdkID,
					},
				},
				Resources: acl.AllResources,
			},
		},
	}

	err = c.UpdateResource(ctx, deviceID, "/oic/sec/acl2", setOwnerAcl, nil)
	if err != nil {
		return fmt.Errorf(errMsg, deviceID, fmt.Errorf("cannot update acl resource owner: %v", err))
	}

	// Provision the device to switch back to normal operation.
	p, err := c.ProvisionDevice(ctx, deviceID)
	if err != nil {
		return fmt.Errorf(errMsg, deviceID, err)
	}
	err = p.Close(ctx)
	if err != nil {
		return fmt.Errorf(errMsg, deviceID, err)
	}

	return nil
}
