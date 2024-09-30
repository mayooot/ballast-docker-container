package container

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/dustin/go-humanize"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"k8s.io/klog"
)

type storageSize int64

func (s storageSize) String() string {
	return strings.Replace(humanize.Bytes(uint64(s)), " ", "", -1)
}

func (s storageSize) Add(delta storageSize) storageSize {
	return storageSize(int64(s) + int64(delta))
}

const (
	ballastPath = "/ballast"

	defaultStorageSize storageSize = 20 * 1000 * 1000 * 1000

	ballastSize storageSize = 5 * 1000 * 1000 * 1000
)

type Container interface {
	Run(name string) (id string, err error)
	Remove(name string) error
	Stop(name string) error
	Start(name string) error
	Close() error
}

type DockerContainer struct {
	cli *client.Client
}

func NewDockerContainer() (Container, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &DockerContainer{cli: cli}, nil
}

func (dc *DockerContainer) Run(name string) (string, error) {
	createResponse, err := dc.cli.ContainerCreate(context.TODO(),
		&container.Config{
			Image:     "ubuntu:latest",
			Cmd:       []string{"sleep", "3600"},
			OpenStdin: true,
			Tty:       true,
			Labels: map[string]string{
				"threshold": defaultStorageSize.Add(ballastSize).String(),
			},
		},
		&container.HostConfig{
			StorageOpt: map[string]string{
				//"size": defaultStorageSize.Add(ballastSize).String(),
			},
		},
		&network.NetworkingConfig{},
		&ocispec.Platform{},
		name,
	)
	if err != nil {
		return "", fmt.Errorf("failed to create container %s: %w", name, err)
	}

	if err := dc.cli.ContainerStart(context.TODO(), createResponse.ID, container.StartOptions{}); err != nil {
		_ = dc.cli.ContainerRemove(context.TODO(), createResponse.ID, container.RemoveOptions{})
		return "", fmt.Errorf("failed to start container %s: %w", name, err)
	}

	cmd := fmt.Sprintf("fallocate -l %s %s", ballastSize.String(), ballastPath)
	klog.Infof("Executing command in container %s: %s", name, cmd)

	if _, err = dc.executeCommand(createResponse.ID, []string{"/bin/bash", "-c", cmd}); err != nil {
		_ = dc.cli.ContainerRemove(context.TODO(), createResponse.ID, container.RemoveOptions{})
		return "", fmt.Errorf("failed to execute command in container %s: %w", name, err)
	}

	klog.Infof("Successfully ran container %s", name)

	return createResponse.ID, nil
}

func (dc *DockerContainer) Remove(name string) error {
	err := dc.cli.ContainerRemove(context.TODO(), name, container.RemoveOptions{Force: true})
	if err != nil && !strings.Contains(err.Error(), "No such container") {
		return fmt.Errorf("failed to remove container %s: %w", name, err)
	}
	return nil
}

func (dc *DockerContainer) Start(name string) error {
	return dc.cli.ContainerStart(context.TODO(), name, container.StartOptions{})
}

// Stop 停止容器并根据磁盘使用情况调整 /ballast 文件
func (dc *DockerContainer) Stop(name string) error {
	var stopFn = func(name string) error {
		timeout := container.StopOptions{}
		if err := dc.cli.ContainerStop(context.TODO(), name, timeout); err != nil {
			return fmt.Errorf("failed to stop container %s: %w", name, err)
		}
		return nil
	}

	size, limited, err := dc.hasStorageLimit(name)
	if err != nil {
		return fmt.Errorf("failed to check container %s: %w", name, err)
	}

	if !limited {
		// 如果容器没有被限制系统盘空间，直接停止容器
		err = stopFn(name)
		if err != nil {
			return fmt.Errorf("failed to stop container %s: %w", name, err)
		}
		return nil
	}

	// 否则容器停止前，检查一下磁盘使用情况
	containerInspect, err := dc.cli.ContainerInspect(context.TODO(), name)
	if err != nil {
		return fmt.Errorf("failed to inspect container %s: %w", name, err)
	}

	dfOutput, err := dc.executeCommand(containerInspect.ID, []string{"df", "--block-size=1G", "/"})
	if err != nil {
		klog.Errorf("Failed to get disk usage for container %s: %v", name, err)
		err = stopFn(name)
		if err != nil {
			return fmt.Errorf("failed to stop container %s: %w", name, err)
		}
		return nil
	}

	// 解析 df 命令的输出
	used, err := parseDfOutput(dfOutput)
	if err != nil {
		klog.Errorf("Failed to parse df output for container %s: %v", name, err)
	} else if size-used <= 1 {
		// 如果磁盘使用情况小于阈值，则调整 /ballast 文件
		// 每次减少 0.5 GB
		// 例如：容器购买时赠送的系统盘大小为 20G，那么实际进行限制的时候是 25G,
		// 当用户使用到了 19G，这时候 df 显示的剩余空间为 1G，就会触发调整 /ballast 的操作
		var reductionGB = 0.5
		klog.Infof("Disk usage %dG >= threshold %dG for container %s, reducing /ballast by %fG", used, size, name, reductionGB)

		if err := adjustBallast(dc, context.TODO(), containerInspect.ID, reductionGB); err != nil {
			klog.Errorf("Failed to adjust /ballast for container %s: %v", name, err)
		}
	}

	// 停止容器
	err = stopFn(name)
	if err != nil {
		return err
	}

	klog.Infof("Successfully stopped container %s", name)

	return nil
}

func (dc *DockerContainer) Close() error {
	return dc.cli.Close()
}

func (dc *DockerContainer) hasStorageLimit(name string) (size int64, hasLimited bool, err error) {
	containerInspect, err := dc.cli.ContainerInspect(context.TODO(), name)
	if err != nil {
		return 0, false, fmt.Errorf("failed to inspect container %s: %w", name, err)
	}

	if v, ok := containerInspect.Config.Labels["threshold"]; !ok {
		return 0, false, nil
	} else {
		size, _ = strconv.ParseInt(strings.Split(v, "GB")[0], 10, 64)
		return size, true, nil
	}
}

// executeCommand 在容器内执行命令并返回输出
func (dc *DockerContainer) executeCommand(containerID string, cmd []string) (string, error) {
	execConfig := types.ExecConfig{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
	}
	execIDResp, err := dc.cli.ContainerExecCreate(context.TODO(), containerID, execConfig)
	if err != nil {
		return "", fmt.Errorf("failed to create exec: %w", err)
	}

	execAttachResp, err := dc.cli.ContainerExecAttach(context.TODO(), execIDResp.ID, types.ExecStartCheck{})
	if err != nil {
		return "", fmt.Errorf("failed to attach exec: %w", err)
	}
	defer execAttachResp.Close()

	output, err := io.ReadAll(execAttachResp.Reader)
	if err != nil {
		return "", fmt.Errorf("failed to read exec output: %w", err)
	}

	execInspect, err := dc.cli.ContainerExecInspect(context.TODO(), execIDResp.ID)
	if err != nil {
		return "", fmt.Errorf("failed to inspect exec: %w", err)
	}
	if execInspect.ExitCode != 0 {
		return "", fmt.Errorf("command exited with code %d: %s", execInspect.ExitCode, string(output))
	}

	return string(output), nil
}

// parseDfOutput 解析 df 命令的输出，返回已用空间（GB）
func parseDfOutput(output string) (int64, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		return 0, fmt.Errorf("unexpected df output format")
	}

	fields := strings.Fields(lines[1])
	if len(fields) < 3 {
		return 0, fmt.Errorf("unexpected df output fields")
	}

	usedStr := fields[2]
	used, err := strconv.ParseInt(usedStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse used disk size: %w", err)
	}

	return used, nil
}

// adjustBallast 调整 /ballast 文件的大小，减少指定的 GB 数量
func adjustBallast(dc *DockerContainer, ctx context.Context, containerID string, reductionGB float64) error {
	// 获取当前 ballast 文件大小
	statOutput, err := dc.executeCommand(containerID, []string{"stat", "-c", "%s", ballastPath})
	if err != nil {
		return fmt.Errorf("failed to get ballast size: %w", err)
	}

	cleanStatOutput := regexp.MustCompile("[^0-9]").ReplaceAllString(statOutput, "")
	ballastSizeBytes, err := strconv.ParseInt(cleanStatOutput, 10, 64)
	if err != nil {
		return fmt.Errorf("failed to parse ballast size: %w", err)
	}

	// 计算新的 ballast 大小（减少 reductionGB）
	reductionBytes := int64(reductionGB * 1000 * 1000 * 1000)
	newBallastSize := ballastSizeBytes - reductionBytes
	if newBallastSize < 0 {
		newBallastSize = 0
	}

	// 删除现有 ballast 文件
	if _, err := dc.executeCommand(containerID, []string{"rm", "-f", ballastPath}); err != nil {
		return fmt.Errorf("failed to remove ballast file: %w", err)
	}

	// 创建新的 ballast 文件（如果新的大小大于 0）
	if newBallastSize > 0 {
		cmd := fmt.Sprintf("fallocate -l %d %s", newBallastSize, ballastPath)
		if _, err := dc.executeCommand(containerID, []string{"/bin/bash", "-c", cmd}); err != nil {
			return fmt.Errorf("failed to create new ballast file: %w", err)
		}
		klog.Infof("Reduced /ballast size to %d bytes", newBallastSize)
	} else {
		klog.Infof("/ballast file removed as new size is %d bytes", newBallastSize)
	}

	return nil
}
