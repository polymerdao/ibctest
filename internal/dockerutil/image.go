package dockerutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
	"go.uber.org/zap"
)

// Image is a docker image.
type Image struct {
	log    *zap.Logger
	client *client.Client

	// NOTE: it might make sense for Image to have an ibc.DockerImage field,
	// but for now it is probably better to not have internal/dockerutil depend on ibc.
	repository, tag string

	networkID string
	testName  string
}

// NewImage returns a valid Image.
//
// "pool" and "networkID" are likely from DockerSetup.
// "testName" is from a (*testing.T).Name() and should match the t.Name() from DockerSetup to ensure proper cleanup.
//
// Most arguments (except tag) must be non-zero values or this function panics.
// If tag is absent, defaults to "latest".
// Currently, only public docker images are supported.
func NewImage(logger *zap.Logger, cli *client.Client, networkID string, testName string, repository, tag string) *Image {
	if logger == nil {
		panic(errors.New("nil logger"))
	}
	if cli == nil {
		panic(errors.New("client cannot be nil"))
	}
	if networkID == "" {
		panic(errors.New("networkID cannot be empty"))
	}
	if testName == "" {
		panic("testName cannot be empty")
	}
	if repository == "" {
		panic(errors.New("repository cannot be empty"))
	}
	if tag == "" {
		tag = "latest"
	}

	i := &Image{
		client:     cli,
		networkID:  networkID,
		repository: repository,
		tag:        tag,
		testName:   testName,
	}
	// Assign log after creating, so the imageRef method can be used.
	i.log = logger.With(
		zap.String("image", i.imageRef()),
		zap.String("test_name", testName),
	)
	return i
}

// ContainerOptions optionally configures starting a Container.
type ContainerOptions struct {
	// bind mounts: https://docs.docker.com/storage/bind-mounts/
	Binds []string

	// Environment variables
	Env []string

	// If blank, defaults to a reasonable non-root user.
	User string
}

// Run creates and runs a container invoking "cmd". The container resources are removed after exit.
//
// Run blocks until the command completes. Thus, Run is not suitable for daemons or servers. Use Start instead.
// A non-zero status code returns an error.
func (image *Image) Run(ctx context.Context, cmd []string, opts ContainerOptions) (stdout, stderr []byte, err error) {
	c, err := image.Start(ctx, cmd, opts)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err := c.Stop(10 * time.Second); err != nil {
			c.log.Error("Failed to stop container", zap.Error(err))
		}
	}()
	return c.Wait(ctx)
}

func (image *Image) imageRef() string {
	return image.repository + ":" + image.tag
}

// ensurePulled can only pull public images.
func (image *Image) ensurePulled(ctx context.Context) error {
	ref := image.imageRef()
	_, _, err := image.client.ImageInspectWithRaw(ctx, ref)
	if err != nil {
		rc, err := image.client.ImagePull(ctx, ref, types.ImagePullOptions{})
		if err != nil {
			return fmt.Errorf("pull image %s: %w", ref, err)
		}
		_, _ = io.Copy(io.Discard, rc)
		_ = rc.Close()
	}
	return nil
}

func (image *Image) createContainer(ctx context.Context, containerName, hostName string, cmd []string, opts ContainerOptions) (string, error) {
	// Although this shouldn't happen because the name includes randomness, in reality there seems to intermittent
	// chances of collisions.

	containers, err := image.client.ContainerList(ctx, types.ContainerListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("name", containerName)),
	})
	if err != nil {
		return "", fmt.Errorf("unable to list containers: %w", err)
	}

	for _, c := range containers {
		if err := image.client.ContainerRemove(ctx, c.ID, types.ContainerRemoveOptions{
			RemoveVolumes: true,
			Force:         true,
		}); err != nil {
			return "", fmt.Errorf("unable to remove container %s: %w", containerName, err)
		}
	}

	// Ensure reasonable defaults.
	if opts.User == "" {
		opts.User = GetDockerUserString()
	}

	cc, err := image.client.ContainerCreate(
		ctx,
		&container.Config{
			Image: image.imageRef(),

			// Entrypoint: []string{}, // Wasn't present before?
			Cmd: cmd,

			Env: opts.Env,

			Hostname: hostName,
			User:     opts.User,

			Labels: map[string]string{CleanupLabel: image.testName},
		},
		&container.HostConfig{
			Binds:           opts.Binds,
			PublishAllPorts: true, // Because we publish all ports, no need to expose specific ports.
			AutoRemove:      false,
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				image.networkID: {},
			},
		},
		nil,
		containerName,
	)
	if err != nil {
		return "", err
	}
	return cc.ID, nil
}

// Start pulls the image if not present, creates a container, and runs it.
func (image *Image) Start(ctx context.Context, cmd []string, opts ContainerOptions) (*Container, error) {
	if len(cmd) == 0 {
		panic(errors.New("cmd cannot be empty"))
	}

	if err := image.ensurePulled(ctx); err != nil {
		return nil, image.wrapErr(err)
	}

	var (
		containerName = SanitizeContainerName(image.testName + "-" + RandLowerCaseLetterString(6))
		hostName      = CondenseHostName(containerName)
		logger        = image.log.With(
			zap.String("command", strings.Join(cmd, " ")),
			zap.String("hostname", hostName),
			zap.String("container", containerName),
		)
	)

	cID, err := image.createContainer(ctx, containerName, hostName, cmd, opts)
	if err != nil {
		return nil, image.wrapErr(fmt.Errorf("create container %s: %w", containerName, err))
	}

	logger.Info("About to start container")

	err = StartContainer(ctx, image.client, cID)
	if err != nil {
		return nil, image.wrapErr(fmt.Errorf("start container %s: %w", containerName, err))
	}

	return &Container{
		Name:        containerName,
		Hostname:    hostName,
		log:         logger,
		image:       image,
		containerID: cID,
	}, nil
}

func (image *Image) wrapErr(err error) error {
	return fmt.Errorf("image %s:%s: %w", image.repository, image.tag, err)
}

// Container is a docker container. Use (*Image).Start to create a new container.
type Container struct {
	Name     string
	Hostname string

	log         *zap.Logger
	image       *Image
	containerID string
}

// Wait blocks until the container exits. Calling wait is not suitable for daemons and servers.
// A non-zero status code returns an error.
//
// Wait implicitly calls Stop.
func (c *Container) Wait(ctx context.Context) (stdout, stderr []byte, err error) {
	waitCh, errCh := c.image.client.ContainerWait(ctx, c.containerID, container.WaitConditionNotRunning)
	var exitCode int
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case err := <-errCh:
		return nil, nil, err
	case res := <-waitCh:
		exitCode = int(res.StatusCode)
		if res.Error != nil {
			return nil, nil, errors.New(res.Error.Message)
		}
	}

	var (
		stdoutBuf = new(bytes.Buffer)
		stderrBuf = new(bytes.Buffer)
	)

	rc, err := c.image.client.ContainerLogs(ctx, c.containerID, types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       "50",
	})
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rc.Close() }()

	// Logs are multiplexed into one stream; see docs for ContainerLogs.
	_, err = stdcopy.StdCopy(stdoutBuf, stderrBuf, rc)
	if err != nil {
		return nil, nil, err
	}
	_ = rc.Close()

	err = c.Stop(10 * time.Second)
	if err != nil {
		c.log.Error("Failed to stop and remove container", zap.Error(err), zap.String("container_id", c.containerID))
	}

	if exitCode != 0 {
		out := strings.Join([]string{stdoutBuf.String(), stderrBuf.String()}, " ")
		return nil, nil, fmt.Errorf("exit code %d: %s", exitCode, out)
	}

	return stdoutBuf.Bytes(), stderrBuf.Bytes(), nil
}

// Stop gives the container up to timeout to stop and remove itself from the network.
func (c *Container) Stop(timeout time.Duration) error {
	// Use timeout*2 to give both stop and remove container operations a chance to complete.
	ctx, cancel := context.WithTimeout(context.Background(), timeout*2)
	defer cancel()

	err := c.image.client.ContainerStop(ctx, c.containerID, &timeout)
	if err != nil {
		// Only return the error if it didn't match an already stopped, or a missing container.
		if !(errdefs.IsNotModified(err) || errdefs.IsNotFound(err)) {
			return c.image.wrapErr(fmt.Errorf("stop container %s: %w", c.Name, err))
		}
	}

	// RemoveContainerOptions duplicates (*dockertest.Resource).Prune.
	err = c.image.client.ContainerRemove(ctx, c.containerID, types.ContainerRemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	})
	if err != nil && !errdefs.IsNotFound(err) {
		return c.image.wrapErr(fmt.Errorf("remove container %s: %w", c.Name, err))
	}

	return nil
}
