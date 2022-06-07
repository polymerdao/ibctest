package polkadot

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	schnorrkel "github.com/ChainSafe/go-schnorrkel/1"
	p2pCrypto "github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/peer"

	"github.com/decred/dcrd/dcrec/secp256k1/v2"
	"github.com/strangelove-ventures/ibctest/ibc"
	"github.com/strangelove-ventures/ibctest/internal/dockerutil"

	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
)

type RelayChainNode struct {
	Home              string
	Index             int
	Chain             ibc.Chain
	NetworkID         string
	Pool              *dockertest.Pool
	Container         *docker.Container
	TestName          string
	Image             ibc.DockerImage
	NodeKey           p2pCrypto.PrivKey
	AccountKey        *schnorrkel.MiniSecretKey
	StashKey          *schnorrkel.MiniSecretKey
	Ed25519PrivateKey p2pCrypto.PrivKey
	EcdsaPrivateKey   secp256k1.PrivateKey
}

type RelayChainNodes []*RelayChainNode

const (
	wsPort         = 27451
	rpcPort        = 27452
	prometheusPort = 27453
)

var exposedPorts = map[docker.Port]struct{}{
	docker.Port(fmt.Sprint(wsPort)):         {},
	docker.Port(fmt.Sprint(rpcPort)):        {},
	docker.Port(fmt.Sprint(prometheusPort)): {},
}

// Name of the test node container
func (p *RelayChainNode) Name() string {
	return fmt.Sprintf("relaychain-%d-%s-%s", p.Index, p.Chain.Config().ChainID, dockerutil.SanitizeContainerName(p.TestName))
}

// Hostname of the test container
func (p *RelayChainNode) HostName() string {
	return dockerutil.CondenseHostName(p.Name())
}

// Dir is the directory where the test node files are stored
func (p *RelayChainNode) Dir() string {
	return filepath.Join(p.Home, p.Name())
}

// MkDir creates the directory for the testnode
func (p *RelayChainNode) MkDir() {
	if err := os.MkdirAll(p.Dir(), 0755); err != nil {
		panic(err)
	}
}

// Bind returns the home folder bind point for running the node
func (p *RelayChainNode) Bind() []string {
	return []string{fmt.Sprintf("%s:%s", p.Dir(), p.NodeHome())}
}

func (p *RelayChainNode) NodeHome() string {
	return fmt.Sprintf("/home/.%s", p.Chain.Config().Name)
}

func (p *RelayChainNode) PeerID() (string, error) {
	id, err := peer.IDFromPrivateKey(p.NodeKey)
	if err != nil {
		return "", err
	}
	return peer.Encode(id), nil
}

func (p *RelayChainNode) GrandpaAddress() (string, error) {
	pubKey, err := p.Ed25519PrivateKey.GetPublic().Raw()
	if err != nil {
		return "", fmt.Errorf("error fetching pubkey bytes: %w", err)
	}
	return EncodeAddressSS58(pubKey)
}

func (p *RelayChainNode) AccountAddress() (string, error) {
	pubKey := make([]byte, 32)
	for i, mkByte := range p.AccountKey.Public().Encode() {
		pubKey[i] = mkByte
	}
	return EncodeAddressSS58(pubKey)
}

func (p *RelayChainNode) StashAddress() (string, error) {
	pubKey := make([]byte, 32)
	for i, mkByte := range p.StashKey.Public().Encode() {
		pubKey[i] = mkByte
	}
	return EncodeAddressSS58(pubKey)
}

func (p *RelayChainNode) EcdsaAddress() (string, error) {
	pubKey := []byte{}
	y := p.EcdsaPrivateKey.PublicKey.Y.Bytes()
	if y[len(y)-1]%2 == 0 {
		pubKey = append(pubKey, 0x02)
	} else {
		pubKey = append(pubKey, 0x03)
	}
	pubKey = append(pubKey, p.EcdsaPrivateKey.PublicKey.X.Bytes()...)
	return EncodeAddressSS58(pubKey)
}

func (p *RelayChainNode) MultiAddress() (string, error) {
	peerId, err := p.PeerID()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/dns4/%s/tcp/%d/p2p/%s", p.HostName(), rpcPort, peerId), nil
}

func (p *RelayChainNode) ChainSpecFilePath() string {
	return filepath.Join(p.Dir(), fmt.Sprintf("%s.json", p.Chain.Config().ChainID))
}

func (p *RelayChainNode) RawChainSpecFilePath() string {
	return filepath.Join(p.Dir(), fmt.Sprintf("%s-raw.json", p.Chain.Config().ChainID))
}

func (p *RelayChainNode) RawChainSpecFilePathContainer() string {
	return filepath.Join(p.NodeHome(), fmt.Sprintf("%s-raw.json", p.Chain.Config().ChainID))
}

func (p *RelayChainNode) GenerateChainSpec(ctx context.Context) error {
	chainCfg := p.Chain.Config()
	cmd := []string{
		chainCfg.Bin,
		"build-spec",
		fmt.Sprintf("--chain=%s", chainCfg.ChainID),
		"--disable-default-bootnode",
	}
	exitCode, stdout, stderr, err := p.NodeJob(ctx, cmd)
	if err != nil {
		return dockerutil.HandleNodeJobError(exitCode, stdout, stderr, err)
	}
	return os.WriteFile(p.ChainSpecFilePath(), []byte(stdout), 0644)
}

func (p *RelayChainNode) GenerateChainSpecRaw(ctx context.Context) error {
	chainCfg := p.Chain.Config()
	cmd := []string{
		chainCfg.Bin,
		"build-spec",
		fmt.Sprintf("--chain=%s.json", filepath.Join(p.NodeHome(), chainCfg.ChainID)),
		"--raw",
	}
	exitCode, stdout, stderr, err := p.NodeJob(ctx, cmd)
	if err != nil {
		return dockerutil.HandleNodeJobError(exitCode, stdout, stderr, err)
	}
	return os.WriteFile(p.RawChainSpecFilePath(), []byte(stdout), 0644)
}

func (p *RelayChainNode) Cleanup(ctx context.Context) error {
	cmd := []string{"find", fmt.Sprintf("%s/.", p.NodeHome()), "-name", ".", "-o", "-prune", "-exec", "rm", "-rf", "--", "{}", "+"}
	return dockerutil.HandleNodeJobError(p.NodeJob(ctx, cmd))
}

func (p *RelayChainNode) CreateNodeContainer() error {
	nodeKey, err := p.NodeKey.Raw()
	if err != nil {
		return fmt.Errorf("error getting ed25519 node key: %w", err)
	}
	multiAddress, err := p.MultiAddress()
	if err != nil {
		return err
	}
	chainCfg := p.Chain.Config()
	cmd := []string{
		chainCfg.Bin,
		fmt.Sprintf("--chain=%s", p.RawChainSpecFilePathContainer()),
		fmt.Sprintf("--ws-port=%d", wsPort),
		fmt.Sprintf("--%s", IndexedName[p.Index]),
		fmt.Sprintf("--node-key=%s", hex.EncodeToString(nodeKey[0:32])),
		"--beefy",
		"--rpc-cors=all",
		"--unsafe-ws-external",
		"--unsafe-rpc-external",
		"--prometheus-external",
		fmt.Sprintf("--prometheus-port=%d", prometheusPort),
		fmt.Sprintf("--listen-addr=/ip4/0.0.0.0/tcp/%d", rpcPort),
		fmt.Sprintf("--public-addr=%s", multiAddress),
		"--base-path", p.NodeHome(),
	}
	fmt.Printf("{%s} -> '%s'\n", p.Name(), strings.Join(cmd, " "))

	cont, err := p.Pool.Client.CreateContainer(docker.CreateContainerOptions{
		Name: p.Name(),
		Config: &docker.Config{
			User:         dockerutil.GetRootUserString(),
			Cmd:          cmd,
			Hostname:     p.HostName(),
			ExposedPorts: exposedPorts,
			DNS:          []string{},
			// Env:          []string{"RUST_BACKTRACE=full"},
			Image:  fmt.Sprintf("%s:%s", p.Image.Repository, p.Image.Version),
			Labels: map[string]string{"ibc-test": p.TestName},
		},
		HostConfig: &docker.HostConfig{
			Binds:           p.Bind(),
			PublishAllPorts: true,
			AutoRemove:      false,
		},
		NetworkingConfig: &docker.NetworkingConfig{
			EndpointsConfig: map[string]*docker.EndpointConfig{
				p.NetworkID: {},
			},
		},
		Context: nil,
	})
	if err != nil {
		return err
	}
	p.Container = cont
	return nil
}

func (p *RelayChainNode) StopContainer() error {
	// timeout is unit of seconds
	return p.Pool.Client.StopContainer(p.Container.ID, 30)
}

func (p *RelayChainNode) StartContainer(ctx context.Context) error {
	if err := p.Pool.Client.StartContainer(p.Container.ID, nil); err != nil {
		return err
	}

	c, err := p.Pool.Client.InspectContainer(p.Container.ID)
	if err != nil {
		return err
	}
	p.Container = c
	return nil
}

// NodeJob run a container for a specific job and block until the container exits
// NOTE: on job containers generate random name
func (p *RelayChainNode) NodeJob(ctx context.Context, cmd []string) (int, string, string, error) {
	counter, _, _, _ := runtime.Caller(1)
	caller := runtime.FuncForPC(counter).Name()
	funcName := strings.Split(caller, ".")
	container := fmt.Sprintf("%s-%s-%s", p.Name(), funcName[len(funcName)-1], dockerutil.RandLowerCaseLetterString(3))
	fmt.Printf("{%s} - [%s:%s] -> '%s'\n", container, p.Image.Repository, p.Image.Version, strings.Join(cmd, " "))
	cont, err := p.Pool.Client.CreateContainer(docker.CreateContainerOptions{
		Name: container,
		Config: &docker.Config{
			User: dockerutil.GetRootUserString(),
			// random hostname is okay here
			Hostname:     dockerutil.CondenseHostName(container),
			ExposedPorts: exposedPorts,
			DNS:          []string{},
			// Env:          []string{"RUST_BACKTRACE=full"},
			Image:  fmt.Sprintf("%s:%s", p.Image.Repository, p.Image.Version),
			Cmd:    cmd,
			Labels: map[string]string{"ibc-test": p.TestName},
		},
		HostConfig: &docker.HostConfig{
			Binds:           p.Bind(),
			PublishAllPorts: true,
			AutoRemove:      false,
		},
		NetworkingConfig: &docker.NetworkingConfig{
			EndpointsConfig: map[string]*docker.EndpointConfig{
				p.NetworkID: {},
			},
		},
		Context: nil,
	})
	if err != nil {
		return 1, "", "", err
	}
	if err := p.Pool.Client.StartContainer(cont.ID, nil); err != nil {
		return 1, "", "", err
	}

	exitCode, err := p.Pool.Client.WaitContainerWithContext(cont.ID, ctx)
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	_ = p.Pool.Client.Logs(docker.LogsOptions{Context: ctx, Container: cont.ID, OutputStream: stdout, ErrorStream: stderr, Stdout: true, Stderr: true, Follow: false, Timestamps: false})
	_ = p.Pool.Client.RemoveContainer(docker.RemoveContainerOptions{ID: cont.ID})
	fmt.Printf("{%s} - stdout:\n%s\n{%s} - stderr:\n%s\n", container, stdout.String(), container, stderr.String())
	return exitCode, stdout.String(), stderr.String(), err
}
