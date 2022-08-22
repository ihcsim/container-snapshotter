package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/images/archive"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
)

const (
	endpoint  = "/run/containerd/containerd.sock"
	imageName = "docker.io/library/nginx:latest"
	namespace = "example"
)

var (
	client *containerd.Client
)

func main() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Kill, os.Interrupt)

	if err := initClients(); err != nil {
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

	container, wait, err := start(ctx, client)
	if err != nil {
		log.Fatal(err)
	}

	archiveFile, err := snapshots(ctx, client, container)
	if err != nil {
		log.Print(err)
	}

	if _, _, err := restore(ctx, archiveFile); err != nil {
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

func initClients() error {
	c, err := containerd.New(endpoint)
	if err != nil {
		return err
	}
	client = c
	return nil
}

func start(ctx context.Context, client *containerd.Client) (containerd.Container, <-chan containerd.ExitStatus, error) {
	image, err := client.Pull(ctx, imageName, containerd.WithPullUnpack)
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

func snapshots(ctx context.Context, client *containerd.Client, container containerd.Container) (*os.File, error) {
	var (
		now          = time.Now().Format("01-02-2006-15:04:05")
		snapshotName = fmt.Sprintf("isim.dev/checkpoint/container/%s:%s", container.ID(), now)
	)

	containerImg, err := container.Checkpoint(
		ctx,
		snapshotName,
		containerd.WithCheckpointRuntime,
		containerd.WithCheckpointTask)
	if err != nil {
		return nil, err
	}
	log.Printf("created container snapshot: %s", containerImg.Name())

	return export(ctx, containerImg)
}

func export(ctx context.Context, image containerd.Image) (*os.File, error) {
	createdAt := image.Metadata().CreatedAt.Format("01-02-2006-15:04:05")
	archiveFile, err := os.Create(fmt.Sprintf("snapshot-%s.tar", createdAt))
	if err != nil {
		return nil, err
	}

	if err := client.Export(
		ctx,
		archiveFile,
		archive.WithImage(client.ImageService(), image.Name())); err != nil {
		return nil, err
	}

	return archiveFile, nil
}

func restore(ctx context.Context, archiveFile *os.File) (containerd.Container, <-chan containerd.ExitStatus, error) {
	// seek to beginning of file in preparation for Import()
	if _, err := archiveFile.Seek(0, 0); err != nil {
		return nil, nil, err
	}

	images, err := client.Import(
		ctx,
		archiveFile)
	if err != nil {
		return nil, nil, err
	}
	log.Printf("imported images from %s", archiveFile.Name())

	img, err := client.GetImage(ctx, images[0].Name)
	if err != nil {
		return nil, nil, err
	}
	log.Printf("found image archive %s", img.Name())

	restored, err := client.Restore(ctx, "restored-nginx", img,
		containerd.WithRestoreImage,
		containerd.WithRestoreSpec,
		containerd.WithRestoreRuntime)
	if err != nil {
		return nil, nil, err
	}
	log.Printf("started container %s", restored.ID())

	task, err := restored.NewTask(ctx, cio.NewCreator(cio.WithStdio))
	if err != nil {
		return nil, nil, err
	}

	wait, err := task.Wait(ctx)
	if err != nil {
		return nil, nil, err
	}

	log.Printf("starting container task %s", task.ID())
	if err := task.Start(ctx); err != nil {
		return nil, nil, err
	}

	return restored, wait, nil
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

	if err := container.Delete(ctx); err != nil {
		return err
	}
	log.Printf("container (%s): msg:'container deleted'", container.ID())

	return nil
}
