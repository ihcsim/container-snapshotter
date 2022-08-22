# Container Snapshot

This is a Go program that snapshots and restores a live OCI container using
the [containerd API][1].

To run this program:

```sh
sudo /usr/local/go/bin/go run ./...
```

If the snapshot and restore completed successfully, the logs will include these
outputs:

```sh
...
2022/08/22 21:03:20 created container snapshot: isim.dev/checkpoint/container/nginx-server:08-22-2022-21:03:20
2022/08/22 21:03:20 imported images from snapshot-08-23-2022-04:03:20.tar
2022/08/22 21:03:20 found image archive isim.dev/checkpoint/container/nginx-server:08-22-2022-21:03:20
2022/08/22 21:03:20 started container restored-nginx
2022/08/22 21:03:20 starting container task restored-nginx
...
```

To see the running containers:

```sh
sudo ctr -n example c ls
```

Both the `nginx-server` and `restored-nginx` containers should be running:

```sh
CONTAINER         IMAGE                             RUNTIME
nginx-server      docker.io/library/nginx:latest    io.containerd.runc.v2
restored-nginx    docker.io/library/nginx:latest    io.containerd.runc.v2
```

Use the `ctr i del` command to remove the snapshot images before restarting.

## Relevant Issues

* https://github.com/containerd/containerd/issues/7193

[1]: https://pkg.go.dev/github.com/containerd/containerd
