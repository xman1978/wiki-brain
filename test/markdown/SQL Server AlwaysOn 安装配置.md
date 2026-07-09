## SQL Server AlwaysOn 安装配置

先期条件
1. 主/备数据库服务器加入 greece.local 域。
2. 主/备数据库服务器私有网络网卡只需要启用“Internet Protocol Version 4(TCP/IP)”组件。
3. 主/备数据库服务器上需要安装 .net framework 3.5 组件和 Failover Cluster 组件。
4. 主/备数据库服务器防火墙上开启 1433/tcp （用于数据库访问）和 5022/tcp （用于 AlwaysOn 数据库同步）端口。
5. 在域控制器上创建共享目录 Quorum 作为 windows 集群的仲裁，创建共享目录 SQLBackup 作为 AlwaysOn 备份目录。
6. 在域控制器上创建保存集群相关对象的 OU（SQLClusters），将主/备数据库服务器对应的计算机账户移入此 OU，并在此 OU 下创建 SQLAdmin 和 SQLService 域账户，以及 SQL Administrators 域安全组。
环境
1. 网络

| 网络 | 地址 | 用途 |
| --- | --- | --- |
| 公有网络 | 10.10.1.0/24 | windows 集群公有网络，用于访问SQL Server 服务 |
| 私有网络 | 172.16.1.0/24 | Windows 集群心跳网络，同时也用于传输 AlwaysOn 同步日志数据 |


2. 服务器

| 服务器 | 域名 | 地址 |
| --- | --- | --- |
| 域控制器 | rome.greece.local | 10.10.1.11 |
| 主数据库服务器 | sql01.greece.local | 10.10.1.101/172.16.1.101 |
| 备用数据库服务器 | sql02.greece.local | 10.10.1.102/172.16.1.102 |


3. 集群地址

| 类型 | 地址 | 域名 |
| --- | --- | --- |
| 集群地址 | 10.10.1.100 | SQLCluster.greece.local |
| AlwaysOn 高可用组侦听地址 | 10.10.1.99 | OA-GAL.greece.local |


4. 域账户/组

| 域账户/组 | 用途 | 权限 |
| --- | --- | --- |
| SQLAdmin | 域账户，用于配置和管理 Windows 集群和SQL Server AlwaysOn | 属于主/备数据库服务器本地管理员组； 对域控制器中用于保存集群机器账户的容器具有完全控制的权限（以方便创建 windows 集群） |
| SQLService | 服务账户，用于 SQL Server 数据库引擎服务和 Agent 服务 | 在主/备数据库服务器上具有 Log on as a service, Lock pages in memory, Perform volume maintenance tasks, Generate security audits, Manage auditing and security log 用户权限 |
| SQL Administrators | 域安全组， 用于管理 SQL Server 数据库 | 具有 SQL Server 数据库管理员权限，SQLAdmin 和 SQLService 为组成员 |
| SQL01$ | 计算机账户，对应主数据库服务器 |  |
| SQL02$ | 计算机账户，对应备用数据库服务器 |  |
| SQLCluster$ | 计算机账户，对应 windows 集群 | 对 OA-GAL 计算机账户拥有完全控制的权限 |
| OA-GAL$ | 计算机账户，对应 AlwaysOn 高可用组侦听 |  |


5. 共享目录

| UNC 路径 | 权限 | 用途 |
| --- | --- | --- |
| \\rome.greece.local\Quorum | 集群对应的计算机账户 SQLCluster$ 对此目录拥有完全控制的权限 | 用于集群仲裁盘 |
| \\rome.greece.local\SQLBackup | SQL Administrators 组对对此目录拥有完全控制的权限 | 用于SQL Server AlwaysOn 备份 |


WINDOWS 集群部署
1. 准备工作
# a. 在域控制器上创建相关账户
PS New-ADOrganizationalUnit -name SqlCluster -path "dc=greece,dc=local"
PS New-ADOrganizationalUnit -name Computers -path "ou=sqlcluster,dc=greece,dc=local"
PS New-ADOrganizationalUnit -name Users -path "ou=sqlcluster,dc=greece,dc=local"
PS New-ADUser -name SQLAdmin  -UserPrincipalName "SQLAdmin@greece.local" -AccountPassword (ConvertTo-SecureString "wisdom4!" -AsPlainText -Force) -PasswordNeverExpires $true -Enable $true -Path "ou=users,ou=sqlcluster,dc=greece,dc=local"
PS New-ADUser -name SQLService  -UserPrincipalName "SQLService@greece.local" -AccountPassword (ConvertTo-SecureString "wisdom4!" -AsPlainText -Force) -PasswordNeverExpires $true -CannotChangePassword $true -Enable $true -Path "ou=users,ou=sqlcluster,dc=greece,dc=local"
PS New-ADGroup -name "SQL Administrators" -GroupScope DomainLocal -Path "ou=users,ou=sqlcluster,dc=greece,dc=local"
PS Add-ADGroupMember -Identity "SQL Administrators" -Members "sqladmin"
PS Add-ADGroupMember -Identity "SQL Administrators" -Members "sqlservice"
C:\> dsacls \\localhost:389\ou=computers,ou=sqlcluster,dc=greece,dc=local /G "cn=sqladmin,ou=users,ou=sqlcluster,dc=greece,dc=local":GA


# b. 在域控制器上创建共享目录用于集群仲裁和数据库备份
C:\> mkdir Quorum
C:\> mkdir SQLBackup
C:\> cacls sqlbackup /E /G
C:\> net share quorum=c:\quorum /grant:everyone,full
C:\> net share sqlbackup=c:\sqlbackup /grant:everyone,full


c. 在主/备数据库服务器上安装 .net framework 3.5 组件和 windows cluster 组件
PS import-module servermanager
PS add-windowsfeature net-framework-features -source d:\sources\sxs
PS add-windowsfeature Failover-Clustering
PS add-windowsfeature RSAT-Clustering-PowerShell


d. 配置主/备数据库服务器网络
C:\>netsh interface ipv4 show interface
Idx     Met         MTU          State                Name
---  ----------  ----------  ------------  ---------------------------
1          50  4294967295  connected     Loopback Pseudo-Interface 1
12          10        1500  connected     Ethernet0
13          10        1500  connected     Ethernet1
C:\>netsh interface ipv4 set address name=12 source=static address=10.10.1.101 mask=255.255.255.0 gateway=10.10.1.2
C:\>netsh interface ipv4 add dnsservers name=12 address=10.10.1.11
C:\>netsh interface ipv4 set address name=13 source=static address=172.16.1.101 mask=255.255.255.0
PS Disable-NetAdapterBinding -Name Ethernet1 -ComponentID ms_rspndr
PS Disable-NetAdapterBinding -Name Ethernet1 -ComponentID ms_lltdio
PS Disable-NetAdapterBinding -Name Ethernet1 -ComponentID ms_implat
PS Disable-NetAdapterBinding -Name Ethernet1 -ComponentID ms_msclient
PS Disable-NetAdapterBinding -Name Ethernet1 -ComponentID ms_pacer
PS Disable-NetAdapterBinding -Name Ethernet1 -ComponentID ms_server
PS Disable-NetAdapterBinding -Name Ethernet1 -ComponentID ms_tcpip6
PS Get-NetAdapterBinding
PS Rename-NetAdapter Ethernet1 eth-private
PS Rename-NetAdapter Ethernet0 eth-public


# e. 设置防火墙规则（允许 1433/TCP，5022/TCP）
C:\>netsh advfirewall firewall add rule name="SQL Server" profile=any dir=in protocol=TCP localport="1433,5022" action=allow


# f. 加入域（greece.local）
C:\>netdom renamecomputer WIN-RJKQMTHPHRD /newname:sql01
C:\>netdom join sql01 /domain:greece.local /userd:administrator /password:wisdom4! /ou:"ou=computers,ou=sqlcluster,dc=greece,dc=local"


# g. 将域账户 加入本地 administrators 组
C:\>net localgroup administrators sqladmin@greece.local /add


# h. 赋用户权限
PS import-module .\UserRights.ps1
PS Grant-UserRight -Account sqlservice@greece.local -Right SeServiceLogonRight
PS Grant-UserRight -Account sqlservice@greece.local -Right SeLockMemoryPrivilege
PS Grant-UserRight -Account sqlservice@greece.local -Right SeManageVolumePrivilege
PS Grant-UserRight -Account sqlservice@greece.local -Right SeAuditPrivilege
PS Grant-UserRight -Account sqlservice@greece.local -Right SeSecurityPrivilege


2. 配置集群
PS import-module FailoverClusters
PS new-cluster -name sqlcluster -node sql01,sql02 -staticaddress 10.10.1.100


3. 配置共享仲裁目录
PS Set-ClusterQuorum -FileShareWitness \\rome.greece.local\quorum -cluster sqlcluster


SQL Server AlwaysOn 安装配置
1. 安装 SQL Server
# a. 安装 SQL 引擎
D:\> .\setup.exe /QS /IACCEPTSQLSERVERLICENSETERMS /ACTION=install /FEATURES=SQL /INSTANCENAME=MSSQLSERVER /SQLSVCACCOUNT="GREECE\SQLService" /SQLSVCPASSWORD="wisdom4!" /SQLSYSADMINACCOUNTS="SQL Administrators" /SECURITYMODE=SQL /SAPWD="wisdom4!" /TCPENABLED=1


# b. 安装 SQL 功能包
C:\> msiexec /i MsSqlCmdLnUtils.msi
C:\> msiexec /i SharedManagementObjects.msi
C:\> msiexec /i PowerShellTools.msi


2. 创建 AlwaysOn 高可用组
# a. 启用 AlwaysOn
C:\> sqlps
PS SQLSERVER:\> cd sql\sql01\default
PS SQLSERVER:\sql\sql01\default> Enable-SqlAlwaysOn


# b. 创建数据库（在 sql01）
C:\> mkdir sqldata
C:\> sqlcmd
1> restore database oa from disk='\\rome.greece.local\sqlbackup\oa.bak'
2> with move 'ezoffice_data' to 'c:\sqldata\oa.mdf',
3> move 'ezoffice_log' to 'c:\sqldata\oa.ldf';
4> go


c. 允许远程访问 SQL Server
C:\> sqlcmd
1> exec sp_configure ‘remote access’,1;
2> go
1> reconfigure;
2> go


d. 允许 SQL Server 账户登录
C:\> sqlcmd
1> alter login sa enable;
2> go
1> alter login sa with password='wisdom4!';
2> go


# e. 创建 OA-GAL 计算机账户
PS C:\> New-ADComputer -name oa-gal -path "ou=computers,ou=sqlcluster,dc=greece,dc=local"
C:\> dsacls \\localhost:389\cn=oa-gal,ou=computers,ou=sqlcluster,dc=greece,dc=local /G "cn=sqlcluster,ou=computers,ou=sqlcluster,dc=greece,dc=local":GA


# f. 备份数据库（在 sql01）
C:\> sqlcmd
1> backup database oa to disk='\\rome.greece.local\sqlbackup\oa.bak';
2> go


# g. 创建高可用组
--- YOU MUST EXECUTE THE FOLLOWING SCRIPT IN SQLCMD MODE.
:Connect sql01
USE [master]
GO
CREATE LOGIN [GREECE\SQLService] FROM WINDOWS
GO
:Connect sql02
USE [master]
GO
CREATE LOGIN [GREECE\SQLService] FROM WINDOWS
GO
:Connect sql01
USE [master]
GO
CREATE ENDPOINT [Hadr_endpoint]
AS TCP (LISTENER_PORT = 5022)
FOR DATA_MIRRORING (ROLE = ALL, ENCRYPTION = REQUIRED ALGORITHM AES)
GO
IF (SELECT state FROM sys.endpoints WHERE name = N'Hadr_endpoint') <> 0
BEGIN
ALTER ENDPOINT [Hadr_endpoint] STATE = STARTED
END
GO
use [master]
GO
GRANT CONNECT ON ENDPOINT::[Hadr_endpoint] TO [GREECE\SQLService]
GO
:Connect sql02
USE [master]
GO
CREATE ENDPOINT [Hadr_endpoint]
AS TCP (LISTENER_PORT = 5022)
FOR DATA_MIRRORING (ROLE = ALL, ENCRYPTION = REQUIRED ALGORITHM AES)
GO
IF (SELECT state FROM sys.endpoints WHERE name = N'Hadr_endpoint') <> 0
BEGIN
ALTER ENDPOINT [Hadr_endpoint] STATE = STARTED
END
GO
use [master]
GO
GRANT CONNECT ON ENDPOINT::[Hadr_endpoint] TO [GREECE\SQLService]
GO
:Connect sql01
IF EXISTS(SELECT * FROM sys.server_event_sessions WHERE name='AlwaysOn_health')
BEGIN
ALTER EVENT SESSION [AlwaysOn_health] ON SERVER WITH (STARTUP_STATE=ON);
END
IF NOT EXISTS(SELECT * FROM sys.dm_xe_sessions WHERE name='AlwaysOn_health')
BEGIN
ALTER EVENT SESSION [AlwaysOn_health] ON SERVER STATE=START;
END
GO
:Connect sql02
IF EXISTS(SELECT * FROM sys.server_event_sessions WHERE name='AlwaysOn_health')
BEGIN
ALTER EVENT SESSION [AlwaysOn_health] ON SERVER WITH (STARTUP_STATE=ON);
END
IF NOT EXISTS(SELECT * FROM sys.dm_xe_sessions WHERE name='AlwaysOn_health')
BEGIN
ALTER EVENT SESSION [AlwaysOn_health] ON SERVER STATE=START;
END
GO
:Connect sql01
USE [master]
GO
CREATE AVAILABILITY GROUP [oa-gal]
WITH (AUTOMATED_BACKUP_PREFERENCE = SECONDARY)
FOR DATABASE [oa]
REPLICA ON N'SQL01' WITH (ENDPOINT_URL = N'TCP://172.16.1.101:5022', FAILOVER_MODE = AUTOMATIC, AVAILABILITY_MODE = SYNCHRONOUS_COMMIT, BACKUP_PRIORITY = 50, SECONDARY_ROLE(ALLOW_CONNECTIONS = NO)),
N'SQL02' WITH (ENDPOINT_URL = N'TCP://172.16.1.102:5022', FAILOVER_MODE = AUTOMATIC, AVAILABILITY_MODE = SYNCHRONOUS_COMMIT, BACKUP_PRIORITY = 50, SECONDARY_ROLE(ALLOW_CONNECTIONS = NO));
GO
:Connect sql01
USE [master]
GO
ALTER AVAILABILITY GROUP [oa-gal]
ADD LISTENER N'oa-gal' (WITH IP((N'10.10.1.99', N'255.255.255.0')), PORT=1433);
GO
:Connect sql02
ALTER AVAILABILITY GROUP [oa-gal] JOIN;
GO
:Connect sql01
BACKUP DATABASE [oa] TO  DISK = N'\\rome.greece.local\sqlbackup\oa.bak' WITH  COPY_ONLY, FORMAT, INIT, SKIP, REWIND, NOUNLOAD, COMPRESSION,  STATS = 5
GO
:Connect sql02
RESTORE DATABASE [oa] FROM  DISK = N'\\rome.greece.local\sqlbackup\oa.bak' WITH  NORECOVERY,  NOUNLOAD,  STATS = 5
GO
:Connect sql01
BACKUP LOG [oa] TO  DISK = N'\\rome.greece.local\sqlbackup\oa_20170125015648.trn' WITH NOFORMAT, NOINIT, NOSKIP, REWIND, NOUNLOAD, COMPRESSION,  STATS = 5
GO
:Connect sql02
RESTORE LOG [oa] FROM  DISK = N'\\rome.greece.local\sqlbackup\oa_20170125015648.trn' WITH  NORECOVERY,  NOUNLOAD,  STATS = 5
GO
:Connect sql02
-- Wait for the replica to start communicating
begin try
declare @conn bit
declare @count int
declare @replica_id uniqueidentifier
declare @group_id uniqueidentifier
set @conn = 0
set @count = 30 -- wait for 5 minutes
if (serverproperty('IsHadrEnabled') = 1)
and (isnull((select member_state from master.sys.dm_hadr_cluster_members where upper(member_name COLLATE Latin1_General_CI_AS) = upper(cast(serverproperty('ComputerNamePhysicalNetBIOS') as nvarchar(256)) COLLATE Latin1_General_CI_AS)), 0) <> 0)
and (isnull((select state from master.sys.database_mirroring_endpoints), 1) = 0)
begin
select @group_id = ags.group_id from master.sys.availability_groups as ags where name = N'oa-gal'
select @replica_id = replicas.replica_id from master.sys.availability_replicas as replicas where upper(replicas.replica_server_name COLLATE Latin1_General_CI_AS) = upper(@@SERVERNAME COLLATE Latin1_General_CI_AS) and group_id = @group_id
while @conn <> 1 and @count > 0
begin
set @conn = isnull((select connected_state from master.sys.dm_hadr_availability_replica_states as states where states.replica_id = @replica_id), 1)
if @conn = 1
begin
-- exit loop when the replica is connected, or if the query cannot find the replica status
break
end
waitfor delay '00:00:10'
set @count = @count - 1
end
end
end try
begin catch
-- If the wait loop fails, do not stop execution of the alter database statement
end catch
ALTER DATABASE [oa] SET HADR AVAILABILITY GROUP = [oa-gal];
GO
GO


# h. 切换角色
ALTER AVAILABILITY GROUP [oa-gal] FAILOVER;
GO


SQL Server AlwaysOn 维护
1. 备份
# a. 备份类型

| 角色 | 备份类型 |
| --- | --- |
| 主副本 | 完全备份，差异备份，日志备份 |
| 备用副本 | 完全备份（with copy_only），日志备份 |


# b. 备份脚本
AlwaysOn 高可用组的通过在各个副本上设置备份优先级确定在哪个副本上执行备份
CREATE PROCEDURE usp_BackupDatabaseAG
(
@DatabaseName SYSNAME,
@BackupPath VARCHAR(256),
@BackupType VARCHAR(4)
)
AS
BEGIN
DECLARE @FileName varchar(512) = @BackupPath +
CAST(@@SERVERNAME AS VARCHAR) + '_' + @DatabaseName
DECLARE @SQLcmd VARCHAR(MAX)
IF sys.fn_hadr_backup_is_preferred_replica(@DatabaseName) = 1
IF @BackupType = 'FULL'
BEGIN
SET @FileName = @FileName + '_FULL_'+
REPLACE(CONVERT(VARCHAR(10), GETDATE(), 112), '/', '') +
REPLACE(CONVERT(VARCHAR(10), GETDATE(), 108) , ':', '')  + '.bak'
SET @SQLcmd = 'BACKUP DATABASE ' + QUOTENAME(@DatabaseName) +
' TO DISK = ''' + @FileName + ''' WITH COPY_ONLY ;'
--PRINT @SQLcmd
EXECUTE(@SQLcmd);
END
ELSE IF @BackupType = 'LOG'
BEGIN
SET @FileName = @FileName + '_LOG_'+
REPLACE(CONVERT(VARCHAR(10), GETDATE(), 112), '/', '') +
REPLACE(CONVERT(VARCHAR(10), GETDATE(), 108) , ':', '')  + '.trn'
SET @SQLcmd = 'BACKUP LOG ' + QUOTENAME(@DatabaseName) +
' TO DISK = ''' + @FileName + ''' ;'
--PRINT @SQLcmd
EXECUTE(@SQLcmd);
END
END


2. 还原
# a. 从高可用组中移除原数据库
USE [master]
GO
ALTER AVAILABILITY GROUP [OA-GA] REMOVE DATABASE [oa];
GO


# b. 从主数据库服务器和备用数据库服务器删除原数据库
USE [master]
GO
DROP DATABASE [oa];
GO


c. 在主数据库服务器上还原新数据库
USE [master]
GO
RESTORE DATABASE [oa] FROM  DISK = N'C:\backup\oa.bak'
GO


d. 将新数据库重新加入高可用组
:Connect SQL01
USE [master]
GO
ALTER AVAILABILITY GROUP [OA-GA] ADD DATABASE [oa];
GO
:Connect SQL01
BACKUP DATABASE [oa] TO  DISK = N'\\rome.greece.local\SQLBackup\oa.bak' WITH  COPY_ONLY, FORMAT, INIT, SKIP, REWIND, NOUNLOAD, COMPRESSION,  STATS = 5
GO
:Connect SQL02
RESTORE DATABASE [oa] FROM  DISK = N'\\rome.greece.local\SQLBackup\oa.bak' WITH  NORECOVERY,  NOUNLOAD,  STATS = 5
GO
:Connect SQL01
BACKUP LOG [oa] TO  DISK = N'\\rome.greece.local\SQLBackup\oa_20170111094735.trn' WITH NOFORMAT, NOINIT, NOSKIP, REWIND, NOUNLOAD, COMPRESSION,  STATS = 5
GO
:Connect SQL02
RESTORE LOG [oa] FROM  DISK = N'\\rome.greece.local\SQLBackup\oa_20170111094735.trn' WITH  NORECOVERY,  NOUNLOAD,  STATS = 5
GO
:Connect SQL02
ALTER DATABASE [oa] SET HADR AVAILABILITY GROUP = [OA-GA];
GO


3. SQL 验证的登录账户
问题：
对于 SQL Server AlwaysOn 高可用组，执行故障转移后，SQL Server 验证的登录账户会丢失映射的数据库账户。
处理：
由于主/备数据库实例上创建的登录账户的 sid 不一致，而这个 sid 会用于数据库账户的映射，因此我们需要在主/备数据库实例上创建登录账户时指定 sid，将其保持一致。
在主数据库实例上，创建登录账户 ezoffice
create login [ezoffice]
with password='12345678',default_database=[oa],default_language=[简体中文],
check_expiration=off,check_policy=off;
go


查询此登录账户的 sid
select suser_sid('ezoffice') -- 0x4B2E3C7DE80FB94BAF40C6F7701ECF94


在备用数据库实例上，创建登录账户 ezoffice，指定相同的 sid
create login [ezoffice]
with password='12345678',sid= 0x4B2E3C7DE80FB94BAF40C6F7701ECF94,
default_database=[oa],default_language=[简体中文],check_expiration=off,
check_policy=off;
go


最后在主数据库实例中映射登录账户
use oa
go
alter user [ezoffice] with login=[ezoffice];
go


4. 日志收缩
Backup log oa to disk='nul';
go
Dbcc shrinkfile(ezoffice_log,8);
go