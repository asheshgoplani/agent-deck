package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	dockermount "github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
)

// DockerRuntime implements ContainerRuntime using the Docker Engine API.
type DockerRuntime struct {
	cli *client.Client
}

// NewDockerRuntime creates a DockerRuntime that connects to the local Docker daemon
// using environment-based configuration (DOCKER_HOST, etc.) with automatic API
// version negotiation.
func NewDockerRuntime() (*DockerRuntime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &DockerRuntime{cli: cli}, nil
}

// Create builds a new container from opts without starting it.
func (d *DockerRuntime) Create(ctx context.Context, opts CreateOpts) (string, error) {
	cfg := &container.Config{
		Image:  opts.Image,
		Cmd:    opts.Cmd,
		Env:    opts.Env,
		Labels: opts.Labels,
	}

	hostCfg := &container.HostConfig{
		Resources: container.Resources{
			NanoCPUs: opts.NanoCPUs,
			Memory:   opts.Memory,
		},
	}

	for _, m := range opts.Mounts {
		hostCfg.Mounts = append(hostCfg.Mounts, dockermount.Mount{
			Type:     dockermount.TypeBind,
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}

	resp, err := d.cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, opts.Name)
	if err != nil {
		return "", fmt.Errorf("container create %q: %w", opts.Name, err)
	}
	return resp.ID, nil
}

// Start starts a previously created container.
func (d *DockerRuntime) Start(ctx context.Context, containerID string) error {
	return d.cli.ContainerStart(ctx, containerID, container.StartOptions{})
}

// Stop gracefully stops a running container. timeoutSecs controls how long to wait
// before sending SIGKILL.
func (d *DockerRuntime) Stop(ctx context.Context, containerID string, timeoutSecs int) error {
	timeout := timeoutSecs
	return d.cli.ContainerStop(ctx, containerID, container.StopOptions{
		Timeout: &timeout,
	})
}

// Remove deletes a container. If force is true, a running container is killed first.
func (d *DockerRuntime) Remove(ctx context.Context, containerID string, force bool) error {
	return d.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{
		Force: force,
	})
}

// Status returns the current state of a container.
func (d *DockerRuntime) Status(ctx context.Context, containerID string) (ContainerState, error) {
	info, err := d.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return ContainerState{Status: StatusNotFound}, nil
		}
		return ContainerState{}, fmt.Errorf("inspect %q: %w", containerID, err)
	}
	if info.State == nil {
		return ContainerState{Status: StatusNotFound}, nil
	}

	st := ContainerState{ExitCode: info.State.ExitCode}
	if info.State.Running {
		st.Status = StatusRunning
	} else {
		st.Status = StatusStopped
	}
	return st, nil
}

// Stats returns point-in-time resource usage for a running container.
func (d *DockerRuntime) Stats(ctx context.Context, containerID string) (ContainerStats, error) {
	resp, err := d.cli.ContainerStats(ctx, containerID, false)
	if err != nil {
		return ContainerStats{}, fmt.Errorf("stats %q: %w", containerID, err)
	}
	defer resp.Body.Close()

	var sr container.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return ContainerStats{}, fmt.Errorf("stats decode: %w", err)
	}

	cpuPct := calculateCPUPercent(sr)
	return ContainerStats{
		CPUPercent: cpuPct,
		MemUsage:   sr.MemoryStats.Usage,
		MemLimit:   sr.MemoryStats.Limit,
	}, nil
}

// calculateCPUPercent computes CPU usage as a percentage from a StatsResponse.
// Formula follows the Docker CLI: delta(container CPU) / delta(system CPU) * numCPUs * 100.
func calculateCPUPercent(sr container.StatsResponse) float64 {
	cpuDelta := float64(sr.CPUStats.CPUUsage.TotalUsage - sr.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(sr.CPUStats.SystemUsage - sr.PreCPUStats.SystemUsage)
	if sysDelta <= 0 || cpuDelta <= 0 {
		return 0.0
	}
	onlineCPUs := float64(sr.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = 1
	}
	return (cpuDelta / sysDelta) * onlineCPUs * 100.0
}

// Exec runs cmd inside a running container, optionally piping stdin, and returns
// the combined stdout+stderr output along with the process exit code.
func (d *DockerRuntime) Exec(ctx context.Context, containerID string, cmd []string, stdin io.Reader) ([]byte, int, error) {
	execCfg := container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
		AttachStdin:  stdin != nil,
	}
	execResp, err := d.cli.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return nil, -1, fmt.Errorf("exec create: %w", err)
	}

	attachResp, err := d.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, -1, fmt.Errorf("exec attach: %w", err)
	}
	defer attachResp.Close()

	if stdin != nil {
		go func() {
			_, _ = io.Copy(attachResp.Conn, stdin)
			_ = attachResp.CloseWrite()
		}()
	}

	var outBuf, errBuf bytes.Buffer
	if _, err := stdcopy.StdCopy(&outBuf, &errBuf, attachResp.Reader); err != nil {
		return nil, -1, fmt.Errorf("exec read: %w", err)
	}

	inspectResp, err := d.cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return nil, -1, fmt.Errorf("exec inspect: %w", err)
	}

	// Combine stdout and stderr into a single output slice.
	combined := outBuf.Bytes()
	if errBuf.Len() > 0 {
		combined = append(combined, errBuf.Bytes()...)
	}
	return combined, inspectResp.ExitCode, nil
}
