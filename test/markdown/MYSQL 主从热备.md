## MYSQL 主从热备

# 1. 原理
a. 主服务器（Master）将数据库的每次更改（SQL 语句）记录到 binlog 二进制日志文件；
b. 从服务器（Slave）会通过主服务器上的同步账户，从主服务器的 binlog 文件中读取这些数据库的更改，将其记录到自己的 replaylog 二进制日志文件；
c. 从服务器从 replaylog 文件中读取更改记录，在自己的数据库上将其重新执行一次。
# 2. lo主从热备配置
## 2.1． 主服务器
a. 创建复制账户
grant replication slave on *.* to 'replay'@'192.168.1.11' identified by 'sql';
--mysql8
create user 'replay'@'192.168.1.11' identified mysql_native_password by 'sql';
grant replication slave,replication client on *.* to 'replay'@'192.168.1.11';
b. 修改 my.cnf 配置
[mysqld]
server-id=1
log-bin=master-binlog
binlog-format=row
binlog-do-db=webapp  # 需要同步的数据库
# 非必须
gtid-mode=on #启用GTID 全局事务
enforce-gtid-consistency=true #启用GTID
master-info-repository=TABLE #默认是file，选择table方式保存
relay-log-info-repository=TABLE #默认是file，选择table方式保存
sync-master-info=1 #实时同步
slave-parallel-workers=2 #设定从服务器的SQL线程数；0表示关闭多线程复制功能
binlog-checksum=CRC32 #日志校验
master-verify-checksum=1 #启用校验
slave-sql-verify-checksum=1 #启用校验
innodb_flush_log_at_trx_commit=1 #每N次事务提交或事务外的指令都需要把日志写入（flush）硬盘
sync_binlog=1 #This makes MySQL synchronize the binary log’s contents to disk each time it commits a transaction


c. 重启 mysql 数据库，查看主服务器状态
mysql> show master status\G
*************************** 1. row ***************************
File: master-binlog.000001
Position: 120
Binlog_Do_DB: webapp
Binlog_Ignore_DB:
Executed_Gtid_Set:
## 2.2. 从服务器
a. 修改 my.cnf 配置
[mysqld]
server-id=2
log-bin=slave-binlog
binlog-format=row
replicate-do-db=webapp
# 非必须
gtid-mode=on #启用GTID,可看结尾的后记2说明
enforce-gtid-consistency=true #启用GTID
master-info-repository=TABLE #默认是file，选择table方式保存
relay-log-info-repository=TABLE #默认是file，选择table方式保存
sync-master-info=1 #实时同步
slave-parallel-workers=2 #设定从服务器的SQL线程数；0表示关闭多线程复制功能
binlog-checksum=CRC32 #日志校验
master-verify-checksum=1 #启用校验
slave-sql-verify-checksum=1 #启用校验
innodb_flush_log_at_trx_commit=1 #每N次事务提交或事务外的指令都需要把日志写入（flush）硬盘
sync_binlog=1 #This makes MySQL synchronize the binary log’s contents to disk each time it commits a transaction


b. 重启 mysql 数据库，使用 change master 语句指向同步位
mysql> stop slave;
mysql> change master to master_host='192.168.1.10',master_port=3328,master_user='replay',master_password='sql',master_log_file='master-binlog.000001',master_log_pos=120;
mysql> start slave;
# master_log_file 和 master_log_pos 参数和主服务器状态中显示的保持一致
c. 查看从服务器状态
mysql> show slave status\G
*************************** 1. row ***************************
Slave_IO_State: Waiting for master to send event
Master_Host: 192.168.1.10
Master_User: replay
Master_Port: 3328
Connect_Retry: 60
Master_Log_File: master-binlog.000001
Read_Master_Log_Pos: 120
Relay_Log_File: my-relay-bin.000003
Relay_Log_Pos: 287
Relay_Master_Log_File: master-binlog.000001
Slave_IO_Running: Yes
Slave_SQL_Running: Yes
Replicate_Do_DB: webapp
Replicate_Ignore_DB:
Replicate_Do_Table:
Replicate_Ignore_Table:
Replicate_Wild_Do_Table:
Replicate_Wild_Ignore_Table:
Last_Errno: 0
Last_Error:
Skip_Counter: 0
Exec_Master_Log_Pos: 120
Relay_Log_Space: 457
Until_Condition: None
Until_Log_File:
Until_Log_Pos: 0
Master_SSL_Allowed: No
Master_SSL_CA_File:
Master_SSL_CA_Path:
Master_SSL_Cert:
Master_SSL_Cipher:
Master_SSL_Key:
Seconds_Behind_Master: 0
Master_SSL_Verify_Server_Cert: No
Last_IO_Errno: 0
Last_IO_Error:
Last_SQL_Errno: 0
Last_SQL_Error:
Replicate_Ignore_Server_Ids:
Master_Server_Id: 1
Master_UUID: 4f2e7d6c-dd09-11e4-a988-000c290f3933
Master_Info_File: /opt/mysql/data/master.info
SQL_Delay: 0
SQL_Remaining_Delay: NULL
Slave_SQL_Running_State: Slave has read all relay log; waiting for the slave I/O thread to update it
Master_Retry_Count: 86400
Master_Bind:
Last_IO_Error_Timestamp:
Last_SQL_Error_Timestamp:
Master_SSL_Crl:
Master_SSL_Crlpath:
Retrieved_Gtid_Set:
Executed_Gtid_Set:
Auto_Position: 0
Slave_IO_Running 和 Slave_SQL_Running 为 Yes 说明已经配置成功，否则检查 Slave_SQL_Running_State 说明的原因如果从库是从主库克隆的，需要修改 data 目录下的 auto.cnf 中的 server-uuid，避免主从库由于 uuid 相同无法同步
# 3. 主主热备配置
## 3.1. 服务器A
a. 创建复制账户
grant replication slave on *.* to 'replay'@'192.168.1.11' identified by 'sql';
b. 配置 my.cnf
[mysqld]
server-id=1
log-bin=master-binlog
binlog-format=row
binlog-do-db=webapp  # 需要同步的数据库
replicate-do-db=webapp
log-slave-updates
sync_binlog = 1
auto_increment_offset = 1
auto_increment_increment = 2


c. 重启 MYSQL，查看状态
mysql> show master status\G
*************************** 1. row ***************************
File: master-binlog.000001
Position: 120
Binlog_Do_DB: webapp
Binlog_Ignore_DB:
Executed_Gtid_Set:
d. 指定同步位置
mysql> stop slave;
mysql> change master to master_host='192.168.1.11',master_port=3328,master_user='replay',master_password='sql',master_log_file='master-binlog.000001',master_log_pos=120;
mysql> start slave;
# master_log_file 和 master_log_pos 参数和服务器B状态中显示 master 的保持一致
mysql> show slave status\G
*************************** 1. row ***************************
Slave_IO_State: Waiting for master to send event
Master_Host: 192.168.1.11
Master_User: replay
Master_Port: 3328
Connect_Retry: 60
Master_Log_File: master-binlog.000001
Read_Master_Log_Pos: 120
Relay_Log_File: my-relay-bin.000002
Relay_Log_Pos: 287
Relay_Master_Log_File: master-binlog.000001
Slave_IO_Running: Yes
Slave_SQL_Running: Yes
Replicate_Do_DB: webapp
Replicate_Ignore_DB:
Replicate_Do_Table:
Replicate_Ignore_Table:
Replicate_Wild_Do_Table:
Replicate_Wild_Ignore_Table:
Last_Errno: 0
Last_Error:
Skip_Counter: 0
Exec_Master_Log_Pos: 120
Relay_Log_Space: 457
Until_Condition: None
Until_Log_File:
Until_Log_Pos: 0
Master_SSL_Allowed: No
Master_SSL_CA_File:
Master_SSL_CA_Path:
Master_SSL_Cert:
Master_SSL_Cipher:
Master_SSL_Key:
Seconds_Behind_Master: 0
Master_SSL_Verify_Server_Cert: No
Last_IO_Errno: 0
Last_IO_Error:
Last_SQL_Errno: 0
Last_SQL_Error:
Replicate_Ignore_Server_Ids:
Master_Server_Id: 1
Master_UUID: 4f2e7d6c-dd09-11e4-a988-000c290f3934
Master_Info_File: /opt/mysql/data/master.info
SQL_Delay: 0
SQL_Remaining_Delay: NULL
Slave_SQL_Running_State: Slave has read all relay log; waiting for the slave I/O thread to update it
Master_Retry_Count: 86400
Master_Bind:
Last_IO_Error_Timestamp:
Last_SQL_Error_Timestamp:
Master_SSL_Crl:
Master_SSL_Crlpath:
Retrieved_Gtid_Set:
Executed_Gtid_Set:
Auto_Position: 0
## 3.2. 服务器B
a. 创建复制账户
grant replication slave on *.* to 'replay'@'192.168.1.11' identified by 'sql';
b. 配置 my.cnf
[mysqld]
server-id=2
log-bin=master-binlog
binlog-format=row
replicate-do-db=webapp
binlog-do-db=webapp
log-slave-updates
sync_binlog = 1
auto_increment_offset = 2
auto_increment_increment = 2


e. 重启 my.cnf，查看状态
mysql> show master status\G
*************************** 1. row ***************************
File: master-binlog.000001
Position: 120
Binlog_Do_DB: webapp
Binlog_Ignore_DB:
Executed_Gtid_Set:
c. 指定同步配置
mysql> stop slave;
mysql> change master to master_host='192.168.1.10',master_port=3328,master_user='replay',master_password='sql',master_log_file='master-binlog.000001',master_log_pos=120;
mysql> start slave;
# master_log_file 和 master_log_pos 参数和服务器A状态中显示 master 的保持一致
mysql> show slave status\G
*************************** 1. row ***************************
Slave_IO_State: Waiting for master to send event
Master_Host: 192.168.1.10
Master_User: replay
Master_Port: 3328
Connect_Retry: 60
Master_Log_File: master-binlog.000001
Read_Master_Log_Pos: 120
Relay_Log_File: my-relay-bin.000002
Relay_Log_Pos: 287
Relay_Master_Log_File: master-binlog.000001
Slave_IO_Running: Yes
Slave_SQL_Running: Yes
Replicate_Do_DB: webapp
Replicate_Ignore_DB:
Replicate_Do_Table:
Replicate_Ignore_Table:
Replicate_Wild_Do_Table:
Replicate_Wild_Ignore_Table:
Last_Errno: 0
Last_Error:
Skip_Counter: 0
Exec_Master_Log_Pos: 120
Relay_Log_Space: 457
Until_Condition: None
Until_Log_File:
Until_Log_Pos: 0
Master_SSL_Allowed: No
Master_SSL_CA_File:
Master_SSL_CA_Path:
Master_SSL_Cert:
Master_SSL_Cipher:
Master_SSL_Key:
Seconds_Behind_Master: 0
Master_SSL_Verify_Server_Cert: No
Last_IO_Errno: 0
Last_IO_Error:
Last_SQL_Errno: 0
Last_SQL_Error:
Replicate_Ignore_Server_Ids:
Master_Server_Id: 2
Master_UUID: 4f2e7d6c-dd09-11e4-a988-000c290f3933
Master_Info_File: /opt/mysql/data/master.info
SQL_Delay: 0
SQL_Remaining_Delay: NULL
Slave_SQL_Running_State: Slave has read all relay log; waiting for the slave I/O thread to update it
Master_Retry_Count: 86400
Master_Bind:
Last_IO_Error_Timestamp:
Last_SQL_Error_Timestamp:
Master_SSL_Crl:
Master_SSL_Crlpath:
Retrieved_Gtid_Set:
Executed_Gtid_Set:
Auto_Position: 0
# 4. Keepalived + 主主热备
## 4.1. 服务器A 配置
/etc/keepalived/keepalived.conf
! Configuration File for keepalived
global_defs {
notification_email {
root@localhost
}
notification_email_from sql@localhost
smtp_server 127.0.0.1
smtp_connect_timeout 30
router_id MYSQL_HA
}
vrrp_script chk_mysql {                   #定义一个外部脚本
script "/etc/keepalived/chk_mysql.sh" #脚本的路径
interval 1                            #通知间隔
weight 2
}
vrrp_instance HA_1 {
state BACKUP   #在DB1和DB2上均配置为BACKUP
interface eth0
virtual_router_id 90
mcast_src_ip 192.168.1.10      #本机 IP 地址
priority 100       #优先级高的为主服务
advert_int 1    #检测时间间隔, 主从服务器保持一致
nopreempt　　　  #不抢占模式，只有优先级高的机器上设置即可，优先级低的机器可不设置
authentication {
auth_type PASS
auth_pass 1234
}
virtual_ipaddress {
192.168.1.100
}
track_script {
chk_mysql
}
}


## 4.2. 服务器 B 配置
5. /etc/keepalived/keepalived.conf
! Configuration File for keepalived
global_defs {
notification_email {
root@localhost
}
notification_email_from sql@localhost
smtp_server 127.0.0.1
smtp_connect_timeout 30
router_id MYSQL_HA
}
vrrp_script chk_mysql {                   #定义一个外部脚本
script "/etc/keepalived/chk_mysql.sh" #脚本的路径
interval 1                            #通知间隔
weight 2
}
vrrp_instance HA_1 {
state BACKUP   #在DB1和DB2上均配置为BACKUP
interface eth0
virtual_router_id 90
mcast_src_ip 192.168.1.11      #本机 IP 地址
priority 100       #优先级高的为主服务
advert_int 1    #检测时间间隔, 主从服务器保持一致
nopreempt　　　  #不抢占模式，只有优先级高的机器上设置即可，优先级低的机器可不设置
authentication {
auth_type PASS
auth_pass 1234
}
virtual_ipaddress {
192.168.1.100
}
track_script {
chk_mysql
}
}


chk_mysql.sh
#!/bin/sh
if [ $(/opt/mysql/bin/mysqladmin -P3328 -uroot -proot ping 2>&1 | grep "mysqld is alive" | wc -l) -eq 0 ]; then
killall keepalived
else
echo 'mysqld is alive'
fi


# 6. 问题
## 6.1. 修改 UUID
主从数据库的 UUID 需要保证不一致，否则就需要修改 auto.cnf 中的 UUID 值。
## 6.2. 自增长主键
auto_increment_offset = 1// 设置AUTO_INCREMENT起点
auto_increment_increment = 10//设置AUTO_INCREMENT增量
主主热备中，因为每台数据库服务器都可能在同一个表中插入数据，如果表有一个自动增长的主键，那么就会在多服务器上出现主键冲突。 解决这个问题的办法就是让每个数据库的自增主键不连续。
假设将来可能需要 10 台服务器做备份， 所以auto-increment-increment 设为10。而 auto-increment-offset=1 表示这台服务器的序号。 从1开始，不超过auto-increment-increment。 这样做之后， 我在这台服务器上插入的第一个id就是 1， 第二行的id就是 11了， 而不是2。（同理，在第二台服务器上插入的第一个id就是2， 第二行就是12， 这个后面再介绍） 这样就不会出现主键冲突了。
## 6.3. 全局事务
由于Mysql 5.6 引入了 GTID(Global Transaction ID)，保证 Slave 在复制的时候不会重复执行相同的事务操作；
其次，是用全局事务 IDs代替由文件名和物理偏移量组成的复制位点，定位 Slave 需要复制的 binlog 内容，在旧的 binlog 事件基础上新增两类事件
1. Previous_gtids_log_event 该事件之前的全局事务 ID 集合
2. Gtid_log_event 标记之后的事务对应的全局事务 ID
MySQL 5.6 的 binlog 文件中，每个事务的开始不是 “BEGIN” ，而是 Gtid_log_event 事件。
使用 GTIDs 作为主备复制的位点，在写 binlog 时用 Gtid_log_event 标记事务；
主从复制不再基于master的binary logfile和logfile postition,从服务器连接到主服务器之后，把自己曾经获取到的GTID(Retrieved_Gtid_Set)发给主服务器，主服务器把从服务器缺少的GTID及对应的transactions发过去即可；
采用多个sql线程，每个sql线程处理不同的database，提高了并发性能，即使某database的某条语句暂时卡住，也不会影响到后续对其它的database进行操作。
## 6.4. 取消主从复制
在从库上执行，将从库脱离主从复制集群
mysql> stop slave;
mysql> reset slave;