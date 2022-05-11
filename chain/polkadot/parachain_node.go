package polkadot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/strangelove-ventures/ibc-test-framework/dockerutil"
	"github.com/strangelove-ventures/ibc-test-framework/ibc"
)

type ParachainNode struct {
	Home            string
	Index           int
	Chain           ibc.Chain
	NetworkID       string
	Pool            *dockertest.Pool
	Container       *docker.Container
	TestName        string
	Image           ibc.ChainDockerImage
	Bin             string
	ChainID         string
	Flags           []string
	RelayChainFlags []string
}

type ParachainNodes []*ParachainNode

// Name of the test node container
func (pn *ParachainNode) Name() string {
	return fmt.Sprintf("%s-%d-%s-%s", pn.Bin, pn.Index, pn.ChainID, dockerutil.SanitizeContainerName(pn.TestName))
}

// Hostname of the test container
func (pn *ParachainNode) HostName() string {
	return dockerutil.CondenseHostName(pn.Name())
}

// Dir is the directory where the test node files are stored
func (pn *ParachainNode) Dir() string {
	return filepath.Join(pn.Home, pn.Name())
}

// MkDir creates the directory for the testnode
func (pn *ParachainNode) MkDir() {
	if err := os.MkdirAll(pn.Dir(), 0755); err != nil {
		panic(err)
	}
}

// Bind returns the home folder bind point for running the node
func (pn *ParachainNode) Bind() []string {
	return []string{fmt.Sprintf("%s:%s", pn.Dir(), pn.NodeHome())}
}

func (pn *ParachainNode) NodeHome() string {
	return fmt.Sprintf("/root/.%s", pn.Chain.Config().Name)
}

func (pn *ParachainNode) RawChainSpecFilePath() string {
	return filepath.Join(pn.Dir(), fmt.Sprintf("%s-raw.json", pn.Chain.Config().ChainID))
}

func (pn *ParachainNode) RawChainSpecFilePathContainer() string {
	return filepath.Join(pn.NodeHome(), fmt.Sprintf("%s-raw.json", pn.Chain.Config().ChainID))
}

type GetParachainIDResponse struct {
	ParachainID int `json:"para_id"`
}

func (pn *ParachainNode) ParachainID(ctx context.Context) (int, error) {
	cmd := []string{
		pn.Bin,
		"build-spec",
		fmt.Sprintf("--chain=%s", pn.ChainID),
	}
	exitCode, stdout, stderr, err := pn.NodeJob(ctx, cmd)
	if err != nil {
		return -1, dockerutil.HandleNodeJobError(exitCode, stdout, stderr, err)
	}
	res := GetParachainIDResponse{}
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		return -1, err
	}
	return res.ParachainID, nil
}

func (pn *ParachainNode) ExportGenesisWasm(ctx context.Context) (string, error) {
	cmd := []string{
		pn.Bin,
		"export-genesis-wasm",
		fmt.Sprintf("--chain=%s", pn.ChainID),
	}
	exitCode, stdout, stderr, err := pn.NodeJob(ctx, cmd)
	if err != nil {
		return "", dockerutil.HandleNodeJobError(exitCode, stdout, stderr, err)
	}
	return stdout, nil
}

func (pn *ParachainNode) ExportGenesisState(ctx context.Context, parachainID int) (string, error) {
	cmd := []string{
		pn.Bin,
		"export-genesis-state",
		fmt.Sprintf("--chain=%s", pn.ChainID),
	}
	exitCode, stdout, stderr, err := pn.NodeJob(ctx, cmd)
	if err != nil {
		return "", dockerutil.HandleNodeJobError(exitCode, stdout, stderr, err)
	}
	return stdout, nil
}

func (pn *ParachainNode) CreateNodeContainer() error {
	cmd := []string{
		pn.Bin,
		fmt.Sprintf("--ws-port=%d", wsPort),
		"--collator",
		"--unsafe-ws-external",
		"--unsafe-rpc-external",
		"--prometheus-external",
		"--rpc-cors=all",
		fmt.Sprintf("--prometheus-port=%d", prometheusPort),
		fmt.Sprintf("--listen-addr=/ip4/0.0.0.0/tcp/%d", rpcPort),
		"--base-path", pn.NodeHome(),
		fmt.Sprintf("--chain=%s", pn.ChainID),
	}
	cmd = append(cmd, pn.Flags...)
	cmd = append(cmd, "--", fmt.Sprintf("--chain=%s", pn.RawChainSpecFilePathContainer()))
	cmd = append(cmd, pn.RelayChainFlags...)
	fmt.Printf("{%s} -> '%s'\n", pn.Name(), strings.Join(cmd, " "))

	cont, err := pn.Pool.Client.CreateContainer(docker.CreateContainerOptions{
		Name: pn.Name(),
		Config: &docker.Config{
			User:         dockerutil.GetRootUserString(),
			Cmd:          cmd,
			Hostname:     pn.HostName(),
			ExposedPorts: exposedPorts,
			DNS:          []string{},
			// Env:          []string{"RUST_BACKTRACE=full"},
			Image:  fmt.Sprintf("%s:%s", pn.Image.Repository, pn.Image.Version),
			Labels: map[string]string{"ibc-test": pn.TestName},
		},
		HostConfig: &docker.HostConfig{
			Binds:           pn.Bind(),
			PublishAllPorts: true,
			AutoRemove:      false,
		},
		NetworkingConfig: &docker.NetworkingConfig{
			EndpointsConfig: map[string]*docker.EndpointConfig{
				pn.NetworkID: {},
			},
		},
		Context: nil,
	})
	if err != nil {
		return err
	}
	pn.Container = cont
	return nil
}

func (pn *ParachainNode) StopContainer() error {
	return pn.Pool.Client.StopContainer(pn.Container.ID, uint(time.Second*30))
}

func (pn *ParachainNode) StartContainer(ctx context.Context) error {
	if err := pn.Pool.Client.StartContainer(pn.Container.ID, nil); err != nil {
		return err
	}

	c, err := pn.Pool.Client.InspectContainer(pn.Container.ID)
	if err != nil {
		return err
	}
	pn.Container = c
	return nil
}

// NodeJob run a container for a specific job and block until the container exits
// NOTE: on job containers generate random name
func (pn *ParachainNode) NodeJob(ctx context.Context, cmd []string) (int, string, string, error) {
	counter, _, _, _ := runtime.Caller(1)
	caller := runtime.FuncForPC(counter).Name()
	funcName := strings.Split(caller, ".")
	container := fmt.Sprintf("%s-%s-%s", pn.Name(), funcName[len(funcName)-1], dockerutil.RandLowerCaseLetterString(3))
	fmt.Printf("{%s} -> '%s'\n", container, strings.Join(cmd, " "))
	cont, err := pn.Pool.Client.CreateContainer(docker.CreateContainerOptions{
		Name: container,
		Config: &docker.Config{
			User: dockerutil.GetRootUserString(),
			// random hostname is okay here
			Hostname:     dockerutil.CondenseHostName(container),
			ExposedPorts: exposedPorts,
			DNS:          []string{},
			// Env:          []string{"RUST_BACKTRACE=full"},
			Image:  fmt.Sprintf("%s:%s", pn.Image.Repository, pn.Image.Version),
			Cmd:    cmd,
			Labels: map[string]string{"ibc-test": pn.TestName},
		},
		HostConfig: &docker.HostConfig{
			Binds:           pn.Bind(),
			PublishAllPorts: true,
			AutoRemove:      false,
		},
		NetworkingConfig: &docker.NetworkingConfig{
			EndpointsConfig: map[string]*docker.EndpointConfig{
				pn.NetworkID: {},
			},
		},
		Context: nil,
	})
	if err != nil {
		return 1, "", "", err
	}
	if err := pn.Pool.Client.StartContainer(cont.ID, nil); err != nil {
		return 1, "", "", err
	}

	exitCode, err := pn.Pool.Client.WaitContainerWithContext(cont.ID, ctx)
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	_ = pn.Pool.Client.Logs(docker.LogsOptions{Context: ctx, Container: cont.ID, OutputStream: stdout, ErrorStream: stderr, Stdout: true, Stderr: true, Tail: "", Follow: false, Timestamps: false})
	_ = pn.Pool.Client.RemoveContainer(docker.RemoveContainerOptions{ID: cont.ID})
	fmt.Printf("{%s} - stdout:\n%s\n{%s} - stderr:\n%s\n", container, stdout.String(), container, stderr.String())
	return exitCode, stdout.String(), stderr.String(), err
}
