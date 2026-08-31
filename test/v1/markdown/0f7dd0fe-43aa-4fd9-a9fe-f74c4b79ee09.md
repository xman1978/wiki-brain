## Oracle 19c RAC 集群安装部署维护

# 一、环境

1. 网络

| 网络 | 地址 | 用途 |
| --- | --- | --- |
| 公共网络 | 10.10.1.0/24 | ORACLE RAC 集群访问地址 |
| 私有网络 | 172.16.1.0/24 | ORACLE RAC 心跳线网络，RAC 心跳线不能直连，需要通过交换机连接 |

2. 服务器

| 服务器 | 机器名 | IP 地址 |
| --- | --- | --- |
| 节点 1 | nrac1 | 10.10.1.101（公共地址） 172.16.1.101（私有地址） 10.10.1.103（虚拟地址） |
| 节点 2 | nrac2 | 10.10.1.102（公共地址） 172.16.1.102（私有地址） 10.10.1.104（虚拟地址） |

3. 软件

| 软件 | 版本 |
| --- | --- |
| 操作系统 | Oracle Linux Server release 7.7 |
| 数据库 | Oracle Enterprise 19.3.0.0 |

# 二、UDEV 磁盘映射

1. 安装相关 rpm 包
rpm -ivh cvuqdisk-1.0.9-1.rpm (在 ORACLE GRID 安装包中)
2. 在一个节点上运行以下命令，获取存储盘的 UID
udevadm info --query=all --name=/dev/sdb | grep ID_SCSI_SERIAL
3. 在两个节点上编辑 udev 配置文件
vi /etc/udev/rules.d/99-oracle-asmdevices.rules
```
KERNEL=="sd*", ENV{ID_SCSI_SERIAL}=="36000c29eaaf46598c460feb89ad3f85d", SYMLINK+="mapper/asm01", OWNER="grid", GROUP="asmadmin", MODE="0660"
KERNEL=="sd*", ENV{ID_SCSI_SERIAL}=="36000c29cfc6c5b010ee21194da3e2698", SYMLINK+="mapper/asm02", OWNER="grid", GROUP="asmadmin", MODE="0660"
KERNEL=="sd*", ENV{ID_SCSI_SERIAL}=="36000c29f4b916c906bdcd71cb91709f9", SYMLINK+="mapper/asm03", OWNER="grid", GROUP="asmadmin", MODE="0660"
KERNEL=="sd*", ENV{ID_SCSI_SERIAL}=="36000c29811f99b3d8cf90886d1b99483", SYMLINK+="mapper/asm03", OWNER="grid", GROUP="asmadmin", MODE="0660"
```

4. 重启 UDEV 服务使配置生效
Redhat 或 Oracle Linux
udevadm trigger --type=devices --action=change (redhat 7)
5. 在两个节点上运行以下命令，验证 UDEV 配置，并比对存储盘是否一一对应
```
ls -l /dev/asm*
brw-rw---- 1 oracle asmadmin 8, 17 Jun 24 16:59 /dev/asm01
brw-rw---- 1 oracle asmadmin 8, 33 Jun 24 16:59 /dev/asm02
brw-rw---- 1 oracle asmadmin 8, 49 Jun 24 16:59 /dev/asm03
brw-rw---- 1 oracle asmadmin 8, 65 Jun 24 16:59 /dev/asm04
ls -l /dev/disk/by-id
lrwxrwxrwx. 1 root root 11 Jun 28 11:17 scsi-22294f64d720fda02 -> ../../asm02
lrwxrwxrwx. 1 root root 11 Jun 28 11:17 scsi-236fb23ee99a717d3 -> ../../asm03
lrwxrwxrwx. 1 root root 11 Jun 28 11:17 scsi-28ef92c2fd91b4b16 -> ../../asm01
lrwxrwxrwx. 1 root root 11 Jun 28 11:17 scsi-2cde1744bbb4ab022 -> ../../asm04
```

6. 可反复重启服务器验证 UDEV 是否配置正确
如磁盘原先被用为 asm disk，可以执行下面命令重新初始化
dd if=/dev/zero of=/dev/sdc bs=1048576 count=50
# 三、AFD 磁盘映射
在没有使用 udev 和 multipath 绑定磁盘的情况下，可以使用 oracle rac 12c 以及以后版本提供的 AFD 绑定。
1. 将 grid 安装包解压到安装目录
以 grid 账户登录系统，执行
unzip -q LINUX.X64_193000_grid_home.zip -d $ORACLE_HOME/
2. 以 root 账户登录系统，设置环境变量
export ORACLE_HOME=/u01/app/19.0.0/grid
export ORACLE_BASE=/tmp
export PATH=$PATH:$ORACLE_HOME/bin
3. 绑定磁盘
asmcmd afd_label DATA1 /dev/sdc --init
asmcmd afd_label DATA2 /dev/sdd --init
asmcmd afd_label DATA3 /dev/sde --init
asmcmd afd_label DATA4 /dev/sdf --init
asmcmd afd_lslbl
给磁盘更改所有者为 grid，以便在安装过程中可以发现磁盘
chown grid:asmadmin /dev/sdc
chown grid:asmadmin /dev/sdd
chown grid:asmadmin /dev/sde
chown grid:asmadmin /dev/sdf
4. 执行 GRID 安装过程
安装 grid 过程中，创建 ASM Disk Group 时注意选择启用 AFD
# 四、ORACLE RAC 安装前准备

1. 安装必要的 rpm 包
Redhat 或 Oracle Linux
bc
binutils
compat-libcap1
compat-libstdc++-33
elfutils-libelf
elfutils-libelf-devel
fontconfig-devel
gcc
gcc-c++
glibc
glibc-devel
ksh
libaio
libaio-devel
libXrender
libXrender-devel
libX11
libXau
libXi
libXtst
libgcc
libstdc++
libstdc++-devel
libxcb
make
net-tools
smartmontools
sysstat
psmisc
bind-utils
ftp
telnet
unzip
xorg-x11-utils
xorg-x11-xauth
libXv
libXt
libXext
libxcb
libXmu
libXxf86dga
libXxf86misc
libXxf86vm
nfs-utils
python (for Oracle ACFS Remote)
python-configshell (for Oracle ACFS Remote)
python-rtslib (for Oracle ACFS Remote)
python-six (for Oracle ACFS Remote)
targetcli (for Oracle ACFS Remote)
cvuqdisk (在 grid 安装包中)
zip
unzip
lsof
rsync
net-tools
telnet
ftp
bind-utils
tree
psmisc
yum install $(awk '{print $1}' ./rpm.txt)
2. 创建 oracle 账户以及相关组
groupadd -g 50000 oinstall
groupadd -g 50001 asmadmin
groupadd -g 50002 asmdba
groupadd -g 50003 asmoper
groupadd -g 50004 dba
groupadd -g 50005 oper
groupadd -g 50006 backupdba
groupadd -g 50007 dgdba
groupadd -g 50008 kmdba
groupadd -g 50009 racdba
useradd -u 50000 -m -g oinstall -G dba,asmadmin,asmdba,asmoper,racdba grid
passwd grid
useradd -u 50001 -m -g oinstall -G dba,oper,asmdba,backupdba,dgdba,kmdba,racdba oracle
passwd oracle
3. 创建安装目录
mkdir -p /u01/app/19.0.0/grid
mkdir -p /u01/app/grid
chown -R grid:oinstall /u01/app/
chmod -R 775 /u01/app
mkdir -p /u01/app/oracle/product/19.0.0/db
chown -R oracle:oinstall /u01/app/oracle
chmod -R 775 /u01/app/oracle
4. 配置 ip 地址映射
vi /etc/hosts
```
#public
10.10.1.101  nrac1
10.10.1.102  nrac2
#private
172.16.1.101 nrac1-priv
172.16.1.102 nrac2-priv
#virtual
10.10.1.103 nrac1-vip
10.10.1.104 nrac2-vip
#scan
10.10.1.100 nrac-scan
```

5. 配置系统内核参数
vi /etc/sysctl.conf
```
fs.aio-max-nr = 1048576
fs.file-max = 6815744
kernel.shmall = 1073741824
kernel.shmmax = 4398046511104
kernel.shmmni = 4096
kernel.sem = 250 32000 100 128
kernel.panic_on_oops = 1
net.ipv4.ip_local_port_range = 9000 65500
net.core.rmem_default = 262144
net.core.rmem_max = 4194304
net.core.wmem_default = 262144
net.core.wmem_max = 1048586
net.ipv4.tcp_wmem = 262144 262144 262144
net.ipv4.tcp_rmem = 4194304 4194304 4194304
```

sysctl –p
6. 修改 ORACLE 账户限制
vi /etc/security/limits.conf
```
grid              soft    nproc   16384
grid              hard    nproc   16384
grid              soft    nofile  1024
grid              hard    nofile  65536
grid              soft    stack   10240
grid              hard    stack   32768
oracle            soft    nproc   16384
oracle            hard    nproc   16384
oracle            soft    nofile  1024
oracle            hard    nofile  65536
oracle            soft    stack   10240
oracle            hard    stack   32768
oracle            hard    memlock 3145728
```

7. 修改 ORACLE 账户环境变量
rac1 node
vi /home/grid/.bash_profile
```
export ORACLE_HOSTNAME=nrac1
export ORACLE_SID=+ASM1
export NLS_LANG=AMERICAN_AMERICA.AL32UTF8
export ORACLE_UNQNAME=nrac
export ORACLE_BASE=/u01/app/grid
export ORACLE_HOME=/u01/app/19.0.0/grid
export PATH=$ORACLE_HOME/bin:/usr/sbin:/sbin:$HOME/bin:$PATH
export LD_LIBRARY_PATH=$ORACLE_HOME/lib:/lib:/usr/lib
export CLASSPATH=$ORACLE_HOME/JRE:$ORACLE_HOME/jlib:$ORACLE_HOME/rdbms/jlib
if [ $USER = "grid" ]; then
if [ $SHELL = "/bin/ksh" ]; then
ulimit -p 16384
ulimit -n 65536
else
ulimit -u 16384 -n 65536
fi
fi
```

vi /home/oracle/.bash_profile
```
export ORACLE_HOSTNAME=nrac1
export ORACLE_SID=nrac1
export NLS_LANG=AMERICAN_AMERICA.AL32UTF8
export ORACLE_UNQNAME=nrac
export ORACLE_BASE=/u01/app/oracle
export ORACLE_HOME=/u01/app/oracle/product/19.0.0/db
export PATH=$ORACLE_HOME/bin:/usr/sbin:/sbin:$HOME/bin:$PATH
export LD_LIBRARY_PATH=$ORACLE_HOME/lib:/lib:/usr/lib
export CLASSPATH=$ORACLE_HOME/JRE:$ORACLE_HOME/jlib:$ORACLE_HOME/rdbms/jlib
if [ $USER = "oracle" ]; then
if [ $SHELL = "/bin/ksh" ]; then
ulimit -p 16384
ulimit -n 65536
else
ulimit -u 16384 -n 65536
fi
fi
```

rac2 node
vi /home/grid/.bash_profile
```
export ORACLE_HOSTNAME=nrac2
export ORACLE_SID=+ASM2
export NLS_LANG=AMERICAN_AMERICA.AL32UTF8
export ORACLE_UNQNAME=nrac
export ORACLE_BASE=/u01/app/grid
export ORACLE_HOME=/u01/app/19.0.0/grid
export PATH=$ORACLE_HOME/bin:/usr/sbin:/sbin:$HOME/bin:$PATH
export LD_LIBRARY_PATH=$ORACLE_HOME/lib:/lib:/usr/lib
export CLASSPATH=$ORACLE_HOME/JRE:$ORACLE_HOME/jlib:$ORACLE_HOME/rdbms/jlib
if [ $USER = "grid" ]; then
if [ $SHELL = "/bin/ksh" ]; then
ulimit -p 16384
ulimit -n 65536
else
ulimit -u 16384 -n 65536
fi
fi
```

vi /home/oracle/.bash_profile
```
export ORACLE_HOSTNAME=nrac2
export ORACLE_SID=nrac2
export NLS_LANG=AMERICAN_AMERICA.AL32UTF8
export ORACLE_UNQNAME=nrac
export ORACLE_BASE=/u01/app/oracle
export ORACLE_HOME=/u01/app/oracle/product/19.0.0/db
export PATH=$ORACLE_HOME/bin:/usr/sbin:/sbin:$HOME/bin:$PATH
export LD_LIBRARY_PATH=$ORACLE_HOME/lib:/lib:/usr/lib
export CLASSPATH=$ORACLE_HOME/JRE:$ORACLE_HOME/jlib:$ORACLE_HOME/rdbms/jlib
if [ $USER = "oracle" ]; then
if [ $SHELL = "/bin/ksh" ]; then
ulimit -p 16384
ulimit -n 65536
else
ulimit -u 16384 -n 65536
fi
fi
```

8. 禁止 ZEROCONF
禁止 zeroconf 配置，防止 Linux 给网卡分配 169.254 地址
/etc/sysconfig/network
NOZEROCONF=yes
9. 确保 NTP 时间同步服务没有被启用，使用 RAC 的 CTSS 同步时间
systemctl disable ntpd
systemctl stop ntpd
mv /etc/ntp.conf /etc/ntp.conf.org
rm /var/run/ntpd.pid
10. 配置 SSH 通道
rac1 node
su - grid
mkdir /home/grid/.ssh
chmod 700 /home/grid/.ssh
cd /home/grid/.ssh
ssh-keygen -t rsa
cat id_rsa.pub >> authorized_keys
scp authorized_keys rac2:/home/grid/.ssh/
rac2 node
su - grid
mkdir /home/grid/.ssh
chmod 700 /home/grid/.ssh
cd /home/grid/.ssh
ssh-keygen -t rsa
cat id_rsa.pub >> authorized_keys
scp authorized_keys rac1:/home/grid/.ssh/
verify
ssh nrac1 hostname
ssh nrac2 hostname
rac1 node
su - oracle
mkdir /home/oracle/.ssh
chmod 700 /home/oracle/.ssh
cd /home/oracle/.ssh
ssh-keygen -t rsa
cat id_rsa.pub >> authorized_keys
scp authorized_keys rac2:/home/oracle/.ssh/
rac2 node
su - oracle
mkdir /home/oracle/.ssh
chmod 700 /home/oracle/.ssh
cd /home/oracle/.ssh
ssh-keygen -t rsa
cat id_rsa.pub >> authorized_keys
scp authorized_keys rac1:/home/oracle/.ssh/
verify
ssh nrac1 hostname
ssh nrac2 hostname
11. 配置 DNS 服务器
如果没有 DNS 服务器，为避免在安装 GRID 过程中验证集群过程出现错误，可以通过修改 nslookup 命令解决。
先将原来的 /usr/bin/nslookup 改名为 nslookup_org
编辑 nslookup 脚本
```
#!/bin/bash
HOSTNAME=${1}
if [[ $HOSTNAME = "rac-scan" ]]; then
echo "Server:         10.10.1.100"
echo "Server:         10.10.1.100#53"
echo ""
echo "Non-authoritative answer:"
echo "Name: nrac-scan"
echo "Address: 10.10.1.100"
elif [[ $HOSTNAME = "nrac1" ]]; then
echo "Server:         10.10.1.101"
echo "Server:         10.10.1.101#53"
echo ""
echo "Non-authoritative answer:"
echo "Name: nrac1"
echo "Address: 10.10.1.101"
elif [[ $HOSTNAME = "nrac2" ]]; then
echo "Server:         10.10.1.102"
echo "Server:         10.10.1.102#53"
echo ""
echo "Non-authoritative answer:"
echo "Name: nrac2"
echo "Address: 10.10.1.102"
else
/usr/bin/nslookup_org $HOSTNAME
fi
```

# 五、安装ORACLE GRID

1. 启动安装脚本
以 grid 账户登录系统，在其中一个节点上运行
cd $ORACLE_HOME && ./gridSetup.sh
注意：修改 $ORACLE_HOME/install/orabasetab 文件(此文件用于记录 ORACLE_HOME 和 ORACLE_BASE)
/u01/app/19.0.0/grid:/u01/app/grid:OraGI19Home1:N:
默认 ORACLE_BASE = /u01/app/oracle ，以 grid 账户安装将会报 error 49802 initializing ADR
2. 选择在集群上安装 Configure Oracle Grid Infrastructure for a New Cluster
3. 选择集群配置- Configure an Oracle Standalone Cluster
4. 配置集群名和 SCAN
5. 配置集群节点和 SSH 连通性
6. 设置公有网和私有网（如果 OCR 和仲裁文件存储在 ASM 盘上，私有网应选择与 ASM 共用）
7. 设置 OCR 和仲裁文件存储位置
8. 选择是否生成 GRID 管理仓库
9. 创建 ASM 磁盘组
如果使用 AFD 映射磁盘注意选择 Configure Oracle ASM Filter Driver
10. 设置 ASM 管理员密码
11. 选择是否使用 IPMI
12. 配置 EM 控制台
13. 配置系统权限组
14. 配置安装目录
15. 配置 oraInventory 路径
16. 选择是否自动运行 root 脚本
17. 验证安装的必要条件
18. 开始安装
19. 安装完毕，运行 root 脚本
20. 重启两个节点，验证 GRID 是否安装正确
crsctl status res -t
```
-------------------------------------------------------------------------------
Name           Target  State        Server                   State details
-------------------------------------------------------------------------------
Local Resources
-------------------------------------------------------------------------------
ora.LISTENER.lsnr
ONLINE  ONLINE       nrac1                    STABLE
ONLINE  ONLINE       nrac2                    STABLE
ora.chad
ONLINE  ONLINE       nrac1                    STABLE
ONLINE  ONLINE       nrac2                    STABLE
ora.net1.network
ONLINE  ONLINE       nrac1                    STABLE
ONLINE  ONLINE       nrac2                    STABLE
ora.ons
ONLINE  ONLINE       nrac1                    STABLE
ONLINE  ONLINE       nrac2                    STABLE
ora.proxy_advm
OFFLINE OFFLINE      nrac1                    STABLE
OFFLINE OFFLINE      nrac2                    STABLE
-------------------------------------------------------------------------------
Cluster Resources
-------------------------------------------------------------------------------
ora.ASMNET1LSNR_ASM.lsnr(ora.asmgroup)
1        ONLINE  ONLINE       nrac1                    STABLE
2        ONLINE  ONLINE       nrac2                    STABLE
3        OFFLINE OFFLINE                               STABLE
ora.DATA.dg(ora.asmgroup)
1        ONLINE  ONLINE       nrac1                    STABLE
2        ONLINE  ONLINE       nrac2                    STABLE
3        OFFLINE OFFLINE                               STABLE
ora.LISTENER_SCAN1.lsnr
1        ONLINE  ONLINE       nrac1                    STABLE
ora.asm(ora.asmgroup)
1        ONLINE  ONLINE       nrac1                    Started,STABLE
2        ONLINE  ONLINE       nrac2                    Started,STABLE
3        OFFLINE OFFLINE                               STABLE
ora.asmnet1.asmnetwork(ora.asmgroup)
1        ONLINE  ONLINE       nrac1                    STABLE
2        ONLINE  ONLINE       nrac2                    STABLE
3        OFFLINE OFFLINE                               STABLE
ora.cvu
1        ONLINE  ONLINE       nrac1                    STABLE
ora.nrac1.vip
1        ONLINE  ONLINE       nrac1                    STABLE
ora.nrac2.vip
1        ONLINE  ONLINE       nrac2                    STABLE
ora.qosmserver
1        ONLINE  ONLINE       nrac1                    STABLE
ora.scan1.vip
1        ONLINE  ONLINE       nrac1                    STABLE
-------------------------------------------------------------------------------
```

# 六、安装 ORACLE 数据库

1. 在其中一个节点上以 oracle 账户启动安装界面
cd $ORACLE_HOME && ./runInstaller
2. 选择在集群上安装 oracle 数据库
3. 选择安装的节点
4. 选择安装类型
5. 选择安装目录
6. 设置系统权限组
7. 选择是否自动执行 ROOT 脚本
8. 检查安装条件
9. 开始安装
10. 安装完毕执行 ROOT 脚本
# 七、创建 ORACLE 实例

1. 切换到 oracle 账户，运行 dbca 启动创建数据库向导
2. 选择部署类型
3. 选择部署节点
4. 设置数据库名和是否 PDB
5. 设置数据库存储位置
6. 设置快速恢复区域
7. 选择安装模块
8. 设置数据库参数
9. 设置 EM 控制台
10. 设置管理账户密码
11. 创建数据库
12. 创建条件检查
13. 开始创建实例
# 八、安装 GRID 和 ORACLE 补丁

p18706488_112030 补丁适用于 GRID 11.2.0.3.0 和 ORACLE 11.2.0.3.0，其中包含 GRID 11.2.0.3.9 和 ORACLE 11.2.0.3.11 补丁集（PSU）
1. 解压补丁
# cd /mnt/patch
# unzip p18706488_112030_Linux-x86-64.zip -d p18706488
# chown -R grid:oinstall p18706488
注意，以 grid 用户身份解压，目录具有 grid 所有
2. 停止 EM 控制台
$ su - oracle
$ emctl stop dbconsole
3. 生成 ocm 响应文件
$ su - root
# cd $ORACLE_HOME/OPatch/ocm/bin
# ./emocmrsp
4. 执行自动升级
$ su – root
# cd $ORACLE_HOME/OPatch
./opatch auto /mnt/patch/p18706488 -ocmrf ./ocm/bin/ocm.rsp -oh /u01/app/11.2.0/grid
5. 执行数据库升级语句
$ su – oracle
$ sqlplus / as sysdba
SQL> @ ?/rdbms/admin/catbundle.sql psu apply
6. 验证升级
$ su – grid
$ cd $ORACLE_HOME/OPatch
$ ./opatch lsinventory
$ su – oracle
$ sqlplus / as sysdba
SQL> select * from dba_registry_history;
# 九、启动停止 RAC 集群

1. 停止数据库
**su – oracle**
$ srvctl stop database -d oarac
2. 停止节点上的数据库实例
**su – oracle**
$ srvctl stop instance -d oarac -i orac1
3. 启动数据库
**su – oracle**
$ srvctl start database -d oarac
4. 启动节点上的数据库实例
**su – oracle**
$ srvctl start instance -d oarac -i orac1
5. 停止集群
**su - grid**
$ crsctl stop resource -all
6. 启动集群
先启动 ASM 实例，确保集群可以访问到存储在 ASM 存储上的 vote disk 和 ocr 记录
# su - grid
$ sqlplus / as sysasm
SQL> startup
启动集群
$ crsctl start resource -all
# 十、验证 RAC 状态
1. 检查集群状态
# su – grid
$ crsctl check crs
2. 检查 ASM 状态
# su - grid
$ srvctl status asm
3. 检查数据库配置
**su - oracle**
$ srvctl config database -d rac
4. 检查数据库状态
**su - oracle**
$ srvctl status database -d rac
5. 验证集群资源运行状态
**su - grid**
$ crsctl status res -t
6. 检查 ASM disk
# su - grid
$ asmcmd
ASMCMD> lsdg
# 十一、开启 RAC 归档
1. 将数据库启动为 mount 模式
$ srvctl stop database -d oa
$ srvctl start database -d oa -o mount
2. 启用归档
$ sqlplus / as sysdba
SQL> alter database archivelog;
3. 启动数据库
$ srvctl stop database -d oa
$ srvctl start database -d oa
# 十二、数据库 REDO 日志
增加 ORACLE RAC redo 日志文件的数量和大小。RAC 中每个数据库实例都有自己的 REDO
alter database add logfile thread 1 group 1 '+DATA' size 200m;
alter database add logfile thread 1 group 3 '+DATA' size 200m;
alter database add logfile thread 1 group 5 '+DATA' size 200m;
alter database add logfile thread 1 group 7 '+DATA' size 200m;
alter database add logfile thread 2 group 2 '+DATA' size 200m;
alter database add logfile thread 2 group 4 '+DATA' size 200m;
alter database add logfile thread 2 group 6 '+DATA' size 200m;
alter database add logfile thread 2 group 8 '+DATA' size 200m;
# 十三、基于策略的 RAC 资源管理
基于策略的管理方式（policy-managed）是以服务池为基础——先定义服务池，为服务指定最小和最大的实例数量，定义管理策略，根据这些策略 oracle 会自动决定有多少实例运行在这个服务池中，服务池与特定数据库服务相关联，用户通过此数据库服务进行访问。区别于，传统的管理员管理方式（admin-managed）是由数据库管理员在安装时手动分配实例给数据库服务。
实例按以下次序分配入服务池中：
服务池分配次序：generic server pool, user assigned server pool, free server pool
1. 首先，按重要性次序分配给所有的服务池，直到满足服务池最小实例数量要求；
2. 如果有剩余的实例，按重要性次序分配给服务池，直到满足服务池最大实例数量要求；
3. 如果有剩余的实例，分配给 free 服务池。
管理：
1. 检查 rac 的资源管理方式
```
$ srvctl config database -d rac
Database unique name: rac
Database name:
Oracle home: /u01/app/oracle/product/11.2.0/db
Oracle user: oracle
Spfile:
Domain:
Start options: open
Stop options: immediate
Database role: PRIMARY
Management policy: AUTOMATIC
Server pools: rac
Database instances: rac1,rac2
Disk Groups: DATA
Mount point paths:
Services:
Type: RAC
Database is administrator managed
```

2. 检查服务池的状态
```
$ srvctl config srvpool
Server pool name: Free
Importance: 0, Min: 0, Max: -1
Candidate server names:
Server pool name: Generic
Importance: 0, Min: 0, Max: -1
Candidate server names: rac1,rac2
$ srvctl config srvpool -g Free
Server pool name: Free
Importance: 0, Min: 0, Max: -1
Candidate server names:
```

3. 添加用户自定义服务池
$ srvctl add srvpool -g oapool -l 2 -u 2 -i 100
将数据库添加到服务池中
$ srvctl modify database -d rac -g oapool
此时 rac 的资源管理方式将变为 policy-managed
创建新的服务
$ srvctl add service -s oa -d rac -g oapool
$ srvctl start service -d rac -s oa
$ srvctl status service -d rac
Service oa is running on nodes: rac1,rac2
4. 将 policy-managed 转换为 admin-managed
不能直接将 policy-managed 转换为 admin-managed，只能删除数据库注册，再重新注册
$ srvctl stop database -d rac
$ srvctl remove database -d rac
$ srvctl add database -d rac -o $ORACLE_HOME
$ srvctl add instance -d rac -i rac1 -n rac1
$ srvctl add instance -d rac -I rac2 -n rac2
$ srvctl start database -d rac
5. rac 运行时负载均衡
在 policy-manage 下，rac 可以配置为在运行过程中动态分配客户端的连接，以实现动态负载均衡。rac 的侦听会根据 PMON 进程动态更新的实例负载情况分发客户端连接。
## a. 设置 remote_listener 和 local_listener
alter system set local_listener=’  (DESCRIPTION=(ADDRESS_LIST=(ADDRESS=(PROTOCOL=TCP) HOST=rac1-vip)(PORT=1521))))’ sid=’oa1’;
alter system set local_listener=’  (DESCRIPTION=(ADDRESS_LIST=(ADDRESS=(PROTOCOL=TCP) HOST=rac2-vip)(PORT=1521))))’ sid=’oa2’;
alter system set remote_listener=’rac-scan:1521’;
## b. 设置服务的连接时间
$ srvctl modify service -s oa -d rac -j SHORT
LONG：一直保持客户端连接在当初分配的实例上，此时 rac 运行时负载均衡将不被使用；SHORT：客户端连接不会一直保持在特定实例上，将根据策略进行分配。
### c. 设置服务的
$ srvctl modify service -s oa -d rac -B throughput
server_time: 根据服务的响应时间来分配客户端连接；throughput：根据服务的吞吐量来分配客户端连接。
$ srvctl config service -s oa -d rac
Service name: oa
Service is enabled
Server pool: oapool
Cardinality: UNIFORM
Disconnect: false
Service role: PRIMARY
Management policy: AUTOMATIC
DTP transaction: false
AQ HA notifications: false
Failover type: NONE
Failover method: NONE
TAF failover retries: 0
TAF failover delay: 0
Connection Load Balancing Goal: SHORT
Runtime Load Balancing Goal: THROUGHPUT
TAF policy specification: NONE
Edition:
Service is enabled on nodes:
Service is disabled on nodes: