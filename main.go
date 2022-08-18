package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
)

const (
	endpoint  = "/run/containerd/containerd.sock"
	namespace = "example"
)

func main() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Kill, os.Interrupt)

	client, err := containerd.New(endpoint)
	if err != nil {
		log.Fatal(err)
	}

	ctx := namespaces.WithNamespace(context.Background(), "example")
	defer func() {
		if err := cleanup(ctx, client); err != nil {
			log.Print(err)
			os.Exit(127)
		}
		client.Close()
	}()

	container, wait, err := startNginx(ctx, client)
	if err != nil {
		log.Fatal(err)
	}

	if err := snapshot(ctx, client, container); err != nil {
		log.Print(err)
	}

	for {
		select {
		case s := <-signals:
			log.Printf("received signal %s", s)
			return
		case status := <-wait:
			code, time, err := status.Result()
			details := fmt.Sprintf("task completed")
			if err != nil {
				details += fmt.Sprintf(" (code:%d, time:%s, err:%v)", code, time, err)
			}
			log.Print(details)
			return
		}
	}
}

func startNginx(ctx context.Context, client *containerd.Client) (containerd.Container, <-chan containerd.ExitStatus, error) {
	image, err := client.Pull(ctx, "docker.io/library/nginx:latest", containerd.WithPullUnpack)
	if err != nil {
		return nil, nil, err
	}
	log.Printf("pulled image: %s", image.Name())

	container, err := client.NewContainer(
		ctx,
		"nginx-server",
		containerd.WithNewSnapshot("nginx-snapshot", image),
		containerd.WithNewSpec(oci.WithImageConfig(image)),
	)
	if err != nil {
		return nil, nil, err
	}

	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
	if err != nil {
		return nil, nil, err
	}

	wait, err := task.Wait(ctx)
	if err != nil {
		return nil, nil, err
	}

	if err := task.Start(ctx); err != nil {
		return nil, nil, err
	}

	return container, wait, nil
}

func snapshot(ctx context.Context, client *containerd.Client, container containerd.Container) error {
	containerImg, err := container.Checkpoint(ctx, "snapshot-checkpoint")
	if err != nil {
		return err
	}
	log.Printf("snapshot: %s", containerImg.Name())

	task, err := container.Task(ctx, nil)
	if err != nil {
		return err
	}
	taskImg, err := task.Checkpoint(ctx)
	if err != nil {
		return fmt.Errorf("failed to checkpoint task %s: %w", task.ID(), err)
	}
	log.Printf("snapshot: %s", taskImg.Name())

	return nil
}

func cleanup(ctx context.Context, client *containerd.Client) error {
	containers, err := client.Containers(ctx)
	if err != nil {
		return err
	}

	var errs []error
	for _, container := range containers {
		if err := remove(ctx, client, container); err != nil {
			errs = append(errs, err)
		}
	}

	var finalErr error
	for _, e := range errs {
		if finalErr == nil {
			finalErr = e
			continue
		}
		finalErr = fmt.Errorf("%s\n%s", finalErr, e)
	}

	return finalErr
}

func remove(ctx context.Context, client *containerd.Client, container containerd.Container) error {
	log.Printf("container (%s): msg:'removing'", container.ID())
	task, err := container.Task(ctx, nil)
	if err != nil {
		return err
	}

	status, err := task.Status(ctx)
	if err != nil {
		return err
	}

	if status.Status == containerd.Running {
		if err := task.Kill(ctx, syscall.SIGKILL); err != nil {
			return err
		}
		log.Printf("container (%s): msg:'process killed'", container.ID())
	}

	wait, err := task.Wait(ctx)
	if err != nil {
		return err
	}
	<-wait

	exit, err := task.Delete(ctx)
	if err != nil {
		return err
	}
	code, time, err := exit.Result()
	details := fmt.Sprintf("container (%s): msg:'task deleted'", container.ID())
	if err != nil {
		details += fmt.Sprintf(" (code:%d, time:%s, err:%v)", code, time, err)
	}
	log.Print(details)

	containerInfo, err := container.Info(ctx)
	if err != nil {
		return err
	}
	if err := containerd.WithSnapshotCleanup(ctx, client, containerInfo); err != nil {
		return err
	}
	log.Printf("container (%s): msg:'snapshot deleted'", container.ID())

	if err := container.Delete(ctx); err != nil {
		return err
	}
	log.Printf("container (%s): msg:'container deleted'", container.ID())

	return nil
}
