# ballast-docker-container

当使用 `--storage-opt size=xGi` 限制容器系统盘大小后，如果磁盘使用满后，会导致 `docker start/restart` 操作失败，
这个项目通过提前设置一个 `/ballast` 文件，提供一个解决思路。

## 前提知识

在售卖容器时，通常会限制容器系统盘大小，也就是 `/` 目录的大小，使用 XFS 文件系统 配合  `--storage-opt` 参数即可实现。

但是当用户长时间使用容器时，容器系统盘会满，一旦容器被停止，再次启动时就会报错。错误信息如下：

```text
Error response from daemon: mkdir /localData/docker/overlay2/7adae703b531d3e114cd171999e5502fe685e13835569b6f1d9fb31ab812773b/merged: disk quota exceeded 
```

当然容器运行过程中这些都属于临时数据，如果用户想要持久化其实使用虚拟机更满足这种场景，而且还很方便的扩容缩容，而且作为一个平台应该
“教育” 用户正确使用容器，并让他们不要把容器当做虚拟机一样使用。

但是我确实在工作中遇到了这种情况，更改项目架构也不可能，所以经过和朋友讨论思考后，想到一种简单的方法来尝试解决这个问题。

## 实现思路

代码实现：[container.go](container.go)

### Run

当用户开通一个容器，我们会限制用户容器的系统盘大小，比如说默认 20Gi
的空间，但是通过程序开通容器时，我们使用 `--storage-opt size=xGi` 限制时会把系统盘大小设置为 20Gi + 5Gi，5Gi 的文件作为一个
ballast（压舱石）存在于用户的容器中。

当用户使用了 19.9Gi（实际情况下要比这个数字更接近 20Gi） 的空间后，`df -h` 显示如下内容：

```bash
$ df -h
Filesystem                             Size    Used   
/                                      25Gi    24.9Gi
```

### Stop

重点在 Stop 时，如果说 Used 已经很接近 Size 了，那么我们就调整 ballast 的大小，保证用户容器正确启动。

> ⚠️ 注意：虽然说容器启动时，可能只需要 几B 的空间，但是无论如何都不应该删除用户容器内的任何数据。

举个例子，当 Used 已经为 24.9Gi，限制的大小为 25Gi，我们 Stop 时，把 ballast 的大小减小 0.5Gi, 保证用户容器正确启动。

## 运行

~~~shell
$ git clone https://github.com/mayooot/ballast-docker-container.git
$ cd ballast-docker-container

$ go mod tidy

$ go test -v -run TestDockerContainerRun
$ go test -v -run TestDockerContainerStop
$ go test -v -run TestDockerContainerStart

$ docker exec -it test stat -c %s /ballast
4500000000

$ go test -v -run TestDockerContainerRemove
~~~

## 贡献

如果对你有帮助，欢迎 star 或者 fork 项目，也欢迎发起 issue 或者 pull request。
