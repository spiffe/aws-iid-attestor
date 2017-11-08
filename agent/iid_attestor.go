package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"path"
	"sync"

	"github.com/hashicorp/go-plugin"
	"github.com/hashicorp/hcl"

	"github.com/spiffe/spire/proto/agent/nodeattestor"
	"github.com/spiffe/spire/proto/common"
	spi "github.com/spiffe/spire/proto/common/plugin"

	"github.com/spiffe/aws-iid-attestor/common"
)

const (
	pluginName           = "iid_attestor"
	identityDocumentUrl  = "http://169.254.169.254/latest/dynamic/instance-identity/document"
	identitySignatureUrl = "http://169.254.169.254/latest/dynamic/instance-identity/signature"
)

type IIDAttestorConfig struct {
	TrustDomain string `hcl:"trust_domain"`
}

type IIDAttestorPlugin struct {
	trustDomain string

	awsAccountId  string
	awsInstanceId string

	mtx *sync.RWMutex
}

type InstanceIdentityDocument struct {
	InstanceId string `json:"instanceId" `
	AccountId  string `json:"accountId"`
}

func (p *IIDAttestorPlugin) spiffeID() *url.URL {
	spiffePath := path.Join("spire", "agent", pluginName, p.awsAccountId, p.awsInstanceId)
	id := &url.URL{
		Scheme: "spiffe",
		Host:   p.trustDomain,
		Path:   spiffePath,
	}
	return id
}

func httpGetBytes(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	bytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

type IidAttestedData struct {
	Document  string `json:"document"`
	Signature string `json:"signature"`
}

func (p *IIDAttestorPlugin) FetchAttestationData(req *nodeattestor.FetchAttestationDataRequest) (*nodeattestor.FetchAttestationDataResponse, error) {
	p.mtx.RLock()
	defer p.mtx.RUnlock()

	docBytes, err := httpGetBytes(identityDocumentUrl)
	if err != nil {
		err = fmt.Errorf("IID attestation attempted but an error occured while retrieving the IID: %v", err)
		return &nodeattestor.FetchAttestationDataResponse{}, err
	}

	var doc InstanceIdentityDocument
	err = json.Unmarshal(docBytes, &doc)
	if err != nil {
		err = fmt.Errorf("IID attestation attempted but an error occured while unmarshalling the IID: %v", err)
		return &nodeattestor.FetchAttestationDataResponse{}, err
	}

	p.awsAccountId = doc.AccountId
	p.awsInstanceId = doc.InstanceId

	sigBytes, err := httpGetBytes(identitySignatureUrl)
	if err != nil {
		err = fmt.Errorf("IID attestation attempted but an error occured while retrieving the IID signature: %v", err)
		return &nodeattestor.FetchAttestationDataResponse{}, err
	}

	attestedData := IidAttestedData{
		Document:  string(docBytes),
		Signature: string(sigBytes),
	}

	respData, err := json.Marshal(attestedData)
	if err != nil {
		err = fmt.Errorf("IID attestation attempted but an error occured while marshaling the attested data: %v", err)
		return &nodeattestor.FetchAttestationDataResponse{}, err
	}

	// FIXME: NA should be the one dictating type of this message
	// Change the proto to just take plain byte here
	data := &common.AttestedData{
		Type: pluginName,
		Data: respData,
	}

	resp := &nodeattestor.FetchAttestationDataResponse{
		AttestedData: data,
		SpiffeId:     p.spiffeID().String(),
	}

	return resp, nil
}

func (p *IIDAttestorPlugin) Configure(req *spi.ConfigureRequest) (*spi.ConfigureResponse, error) {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	resp := &spi.ConfigureResponse{}

	// Parse HCL config payload into config struct
	config := &IIDAttestorConfig{}
	hclTree, err := hcl.Parse(req.Configuration)
	if err != nil {
		resp.ErrorList = []string{err.Error()}
		return resp, err
	}
	err = hcl.DecodeObject(&config, hclTree)
	if err != nil {
		resp.ErrorList = []string{err.Error()}
		return resp, err
	}

	// Set local vars from config struct
	p.trustDomain = config.TrustDomain

	return resp, nil
}

func (*IIDAttestorPlugin) GetPluginInfo(*spi.GetPluginInfoRequest) (*spi.GetPluginInfoResponse, error) {
	return &spi.GetPluginInfoResponse{}, nil
}

func New() nodeattestor.NodeAttestor {
	return &IIDAttestorPlugin{
		mtx: &sync.RWMutex{},
	}
}

func main() {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: nodeattestor.Handshake,
		Plugins: map[string]plugin.Plugin{
			pluginName: nodeattestor.NodeAttestorPlugin{NodeAttestorImpl: New()},
		},
		GRPCServer: plugin.DefaultGRPCServer,
	})
}