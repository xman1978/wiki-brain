Docker Swam 集群部署架构
Docker Swarm 的架构包括 Manager 节点、Worker 节点、Overlay 网络、Service 和 Task。其中，Manager 节点负责集群管理、调度和控制，Worker 节点负责运行容器，Overlay 网络提供容器间的通信和服务发现，Service 定义容器的副本数、镜像、网络等信息，Task 是 Service 在节点上的运行实例。些组件共同协作，实现了 Docker 容器的高效编排和管理。
在 Swarm 集群中，Manager 节点可以有多个，但只有一个 Leader 节点，负责整个集群的管理和调度。Worker 节点可以有多个，它们负责运行容器，并向 Manager 节点报告自己的状态。Overlay 网络是一种虚拟网络，它允许容器在不同节点之间进行通信，同时提供了服务发现的功能。
Service 是 Swarm 集群中最重要的概念之一，它定义了容器的副本数、镜像、网络等信息。当 Service 创建时，它会在集群中的某些节点上创建 Task，每个 Task 对应一个容器实例。Swarm 集群会自动将 Task 分配到可用的 Worker 节点上，并确保容器的副本数达到预期值。如果某个节点出现故障或容器崩溃，Swarm 集群会自动重新分配 Task，并确保容器的副本数恢复到预期值。
相关概念Manager 节点：负责集群管理、调度和控制。
Worker 节点：负责运行容器。
Overlay 网络：提供容器间的通信和服务发现。
Service：定义容器的副本数、镜像、网络等信息。
Stack：一组关联的服务，可以通过 Compose 文件来定义。
Task：Service 在节点上的运行实例。
Swarm 集群：由多个 Manager 和 Worker 节点组成的集群。
网络机制Docker Swarm 集群网络机制包括两个层次：overlay 网络和 ingress 网络。
Overlay 网络Overlay 网络是一个基于 VXLAN 的虚拟网络，它允许在 Docker Swarm 集群中的不同节点之间创建多个子网，并将不同的服务和容器连接到这些子网上。在一个 Overlay 网络中，每个节点都有一个 VXLAN 设备，用于将数据包封装和解封装。当容器或服务需要与其他节点上的容器或服务通信时，数据包将通过 Overlay 网络传输，其中源和目标 IP 地址都是容器或服务的虚拟 IP 地址。
Overlay 网络可以通过 Docker CLI 或 Docker API 来创建和管理。可以为每个 Overlay 网络指定一个唯一的名称，并为其分配一个子网范围。可以使用 Docker Compose 文件或 Docker Stack 文件来定义 Overlay 网络中的服务和容器，并为它们指定虚拟 IP 地址。
Ingress 网络Ingress 网络是一个特殊的 Overlay 网络，用于将外部流量路由到 Docker Swarm 集群中的服务。当一个服务在 Docker Swarm 集群中创建时，它可以选择加入 Ingress 网络，并使用一个唯一的主机名来标识自己。当外部客户端请求这个主机名时，Docker Swarm 集群将自动将请求路由到正确的服务。
当任何节点在发布的端口上接收到请求时，Ingress 将该请求交给一个名为 IPVS 的模块。IPVS 跟踪参与该服务的所有IP地址，选择其中的一个，并通过 ingress 网络将请求路由到它。
Ingress 网络可以使用 Docker CLI 或 Docker API 来创建和管理。可以为每个 Ingress 网络指定一个唯一的名称，并为其分配一个子网范围。可以使用 Docker Compose 文件或 Docker Stack 文件来定义 Ingress 网络中的服务，并为它们指定主机名。
docker_gwbridge 网络是一种桥接网络，将 overlay 网络（包括 ingress 网络）连接到一个单独的 Docker 守护进程的物理网络。
默认情况下，服务正在运行的每个容器都连接到本地 Docker 守护进程主机的 docker_gwbridge 网络。
docker_gwbridge 网络在初始化或加入 Swarm 时自动创建。大多数情况下，用户不需要自定义配置，但是 Docker 允许自定义。
Docker Swarm 集群部署
软件版本
Centos stream 9
Docker-ce 24.0.2
环境准备
在所有节点上执行

1. 关闭防火墙
   关闭防火墙
   systemctl stop firewalld
   systemctl disable firewalld

或在防火墙上开发下列端口2377/TCP ：集群管理端口7946/TCP ：节点之间通讯端口（不开放则会负载均衡失效）4789/UDP ：overlay网络通讯端口
firewall-cmd --add-port 2377/tcp --permanent
firewall-cmd --add-port 7946/tcp --permanent
firewall-cmd --add-port 4789/udp --permanent
firewall-cmd --reload

2. 关闭 selinux
   sed -i 's/enforcing/disabled/g' /etc/selinux/config

重启服务器
3. 调整内核参数
编辑 /etc/sysctl.conf 文件，添加
fs.aio-max-nr = 1048576
fs.file-max = 6815744
kernel.shmall = 2097152
kernel.shmmax = 4294967295
kernel.shmmni = 4096
kernel.sem = 250 32000 100 128
net.ipv4.ip_local_port_range = 1024 65500
net.ipv4.tcp_keepalive_time = 30
net.ipv4.tcp_tw_reuse = 1
net.ipv4.tcp_syncookies = 1
net.core.rmem_default = 262144
net.core.rmem_max = 4194304
net.core.wmem_default = 262144
net.core.wmem_max = 1048576
net.core.somaxconn = 2048
kernel.pid_max = 1000000

vm.max_map_count = 262144

vm.swappiness = 10

执行 sysctl -p 命令使之生效编辑 /etc/security/limits.conf 文件，添加

* soft nofile 65536 
* hard nofile 65536 
* soft nproc 65536 
* hard nproc 65536

用户重新登录后生效
4. 配置 docker-ce 国内源
step 1: 安装必要的一些系统工具
sudo yum install -y yum-utils device-mapper-persistent-data lvm2
Step 2: 添加软件源信息
sudo yum-config-manager --add-repo https://mirrors.aliyun.com/docker-ce/linux/centos/docker-ce.repo
Step 3
sudo sed -i 's+download.docker.com+mirrors.aliyun.com/docker-ce+' /etc/yum.repos.d/docker-ce.repo
Step 4: 更新并安装Docker-CE
sudo yum makecache

5. 安装 docker-ce
   yum install docker-ce
   systemctl start docker
   systemctl enable docker

主 Manager 节点部署（leader）
docker swarm init
注：如果主机拥有多个IP，需要使用 --advertise-addr 指定 IP。
docker swarm init --advertise-addr 192.168.99.100

加入从 Manager 节点在主 Master 节点上执行，获取加入从 Master 节点的命令
docker swarm join-token manager
在从 Master 节点上执行，加入从 Master 节点
docker swarm join --token SWMTKN-1-22921lkf4gy74u1ovey2cwmaf8s6yh774oqqzcmiswq3z9fpjg-6e4mc8y4jfslmzdubg99q0999 192.168.99.100:2377

加入 Worker 节点
在主 Master 节点上执行，获取加入命令
docker swarm join-token worker
在从 Master 节点上执行，加入从 Master 节点
docker swarm join --token SWMTKN-1-22921lkf4gy74u1ovey2cwmaf8s6yh774oqqzcmiswq3z9fpjg-2z2okt46vuensumj8voy4lsya 192.168.99.100:2377

检查节点状态
在主 Master 节点上执行
docker node list

AVAILABILITY 列，表示调度程序是否可以将任务分配给节点，有下列状态：
Active：可以将任务分配给节点。
Pause：不向节点分配新任务，但现有的任务仍然运行。
Drain：不向节点分配新任务，已经存在的任务也将被调用到Active节点上。
MANAGER STATUS列，表示管理节点状态，有下列状态：
Leader：为集群做出所有的集群管理和编排决策。
Reachable：表示节点参与Raft仲裁的manager节点。如果leader节点不可用，则该节点有资格成为新的leader。
Unavailable：表示节点是一个无法与其他manager通信的节点。如果manager节点变为此状态应该加入一个新的manager节点到集群中，或者将一个工作节点提升为一个manager。
没有值表示不参与群集管理的工作节点，
管理节点
查看运行的一个或多个及节点任务数
docker node ps nodename
将worker角色升级为manager
docker node promote nodename
将manager角色降级为worker
docker node demote nodename
查看节点的详细信息
docker node inspect nodename
从集群中删除一个节点
docker node rm nodename
更新一个节点
docker node update --availability active id
docker node update --availability pause id
docker node update --availability drain id

删除节点

移除一个work-node节点的步骤：

1.在Manager节点上操作，清空worker节点的容器。id 可以使用命令 docker node ls 查看
docker node update --availability drain id
2.在worker节点主机上操作，退出集群
docker swarm leave
3，在 Manager节点上操作，删除work-node节点
docker node rm id
若想解散整个集群，则需先移除所有work-node节点主机，然后所有管理节点也退出集群

Stack 部署应用stack 是一组共享依赖，可以被编排并具备扩展能力的关联 service
在 Docker Swarm 中使用 Stack 配置文件部署应用的步骤如下：
编写 Stack 配置文件
在编写 Stack 配置文件时，需要指定应用程序的服务和网络等信息。以下是一个部署 portainer 的示例 Stack 配置文件：
version: '3.2'
services:
agent:
image: portainer/agent:2.18.3
volumes:

- /var/run/docker.sock:/var/run/docker.sock
- /var/lib/docker/volumes:/var/lib/docker/volumes
  networks:
- agent_network
  deploy:
  mode: global
  placement:
  constraints: [node.platform.os == linux]
  portainer:
  image: portainer/portainer-ce:2.18.3
  command: -H tcp://tasks.agent:9001 --tlsskipverify
  ports:
- "9443:9443"
- "9000:9000"
- "8000:8000"
  volumes:
- /opt/portainer/data:/data
  networks:
- agent_network
  deploy:
  mode: replicated
  replicas: 1
  placement:
  constraints: [node.role == manager]
  networks:
  agent_network:
  driver: overlay
  attachable: true
  volumes:
  portainer_data:

在上面的配置文件中，定义了两个名为 agent 和 portainer 的服务。
以 agent 服务为例，image 指定其使用的是 portainer/agent 镜像，volumes 指定其将主机的 /var/run/docker.sock 文件和 /var/lib/docker/volumes 目录映射到容器中对应的文件和目录，networks 指定其使用的是 agent_network overlay 网络。
Docker Stack 配置文件是一个 YAML 格式的文件，用于定义 Docker Stack 中的服务、网络和卷等元素。以下是 Stack 配置文件中所有的元素：

- version：定义 Stack 文件的版本。
- services：定义 Stack 中的服务。每个服务都包含一个或多个容器实例。
- networks：定义 Stack 中的网络。网络用于连接服务和容器之间的通信。
- volumes：定义 Stack 中使用的卷。卷用于在容器之间共享数据。
- configs：定义 Stack 中使用的配置。配置用于存储应用程序的配置数据，例如环境变量和配置文件。
- secrets：定义 Stack 中使用的机密数据。机密数据用于存储敏感信息，例如密码和证书。
- deploy：定义 Stack 的部署选项。可以使用该选项指定服务的副本数、部署策略和节点约束等。
- external：定义 Stack 使用的外部资源。可以使用该选项引用外部的网络、卷和服务等。
  部署应用程序
  使用以下命令部署应用程序：
  docker stack deploy -c <stack-file> <stack-name>
  其中，<stack-file> 是 Stack 配置文件的路径，<stack-name> 是应用程序的名称。
  例如，如果 Stack 配置文件名为 webapp.yml，应用程序名称为 `webapp`，则可以使用以下命令部署应用程序：
  docker stack deploy -c webapp.yml webapp
  查看应用程序状态
  使用以下命令查看应用程序的状态：
  docker stack ps <stack-name>
  其中，<stack-name> 是应用程序的名称。
  例如，如果应用程序名称为 webapp，则可以使用以下命令查看应用程序状态：
  docker stack ps webapp
  以上就是在 Docker Swarm 中使用 Stack 配置文件部署应用的基本步骤。你可以根据实际情况修改 Stack 配置文件来满足不同的需求。