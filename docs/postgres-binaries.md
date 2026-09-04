# PostgreSQL 二进制获取逻辑

与 MySQL 不同，**postgresql.org 官方不发布预编译二进制**——官网下载页只导向
第三方渠道。dbpod 按平台路由：

| 平台 | 来源 | 形态 |
| --- | --- | --- |
| Windows | EDB `postgresql-<ver>-windows-x64-binaries.zip` | zip 单跳解压 |
| macOS | EDB `postgresql-<ver>-osx-binaries.zip`（universal，无 arch 段）| zip 单跳解压 |
| Linux | **PGDG 仓库提取管道**（apt + yum 双仓库）| .deb/.rpm 解包提纯 |

EDB（EnterpriseDB，PostgreSQL 的商业维护者）的便携 zip 无官方 per-file
checksum 文件，dbpod 通过 HEAD 探测确认存在性；如需完整性校验，可在
manifest（`postgres.json`）中手工补充 sha256。

## Linux：PGDG 基线矩阵

| 基线 | glibc | PG 覆盖 |
| --- | --- | --- |
| el7（RHEL/CentOS 7）| 2.17 | ≤ 16 |
| el8 | 2.28 | 12–18 |
| el9 | 2.34 | 13–18 |
| bookworm-pgdg（apt）| 2.36 | 全部 |
| noble-pgdg（apt）| 2.39 | 全部 |

默认选择**满足目标 PG 版本的最低 glibc 基线**（低版本 glibc 二进制在高版本
系统上向前兼容）：PG ≤16 → el7；PG 17/18 → el8。

### .deb 提取管道

1. 版本清单：`dists/{codename}-pgdg/main/binary-{amd64,arm64}/Packages.gz`
   解析 `Package:/Version:/Filename:` 字段
2. 下载 server/client 主包 + 运行时依赖包（索引 `Depends:` 驱动的映射矩阵：
   libssl3/libicu/libreadline/liblz4/libzstd/libpq5 等；**glibc 不捆绑**）
3. .deb（ar 归档）→ `data.tar.xz` → 前缀规则映射提取：
   - `usr/lib/postgresql/<maj>/bin/*` → `bin/`
   - `usr/lib/postgresql/<maj>/lib/*` → `lib/`
   - `usr/share/postgresql/<maj>/*` → `share/`
   - `usr/lib/<arch>-linux-gnu/*.so*` → `shared_libs/`

### .rpm 提取管道

yum 仓库 `yum/<maj>/redhat/rhel-<base>-<arch>/repodata/primary.xml.gz` 定位
rpm（el7 的 x86_64 包在 `yum/common/` 下）→ rpm 头 + cpio payload 解压 →
`/usr/pgsql-<maj>/{bin,lib,share}` 映射提取。

### 运行期隔离

引擎目录的 `shared_libs/` 与 `lib/` 会注入 `LD_LIBRARY_PATH`（monitor 启动、
initdb、psql/exec 全部生效），实现免依赖解压即跑。
