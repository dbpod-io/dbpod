# MySQL 下载链接获取逻辑

本文档描述 dbpod 的引擎二进制包版本信息与下载链接的获取机制。
对 MySQL 官网页面的解析（爬取）仅发生在维护者侧的生成工具 `cmd/metadata-gen` 中，
CLI 常规流程通过仓库 CDN 或二进制内嵌数据获取元信息（见第 4 节）。
无需官方 API——所有信息均来自公开网页，经 HTML 解析获得。

## 1. 数据源

MySQL 官方分发渠道有两个互补的页面：

| 页面 | URL | 覆盖范围 |
| --- | --- | --- |
| GA 下载页（最新版本） | `https://dev.mysql.com/downloads/mysql/` | 仅当前最新的几个版本系列（如 26.7 / 9.7 LTS / 8.4 LTS / 8.0） |
| 历史归档页 | `https://downloads.mysql.com/archives/community/` | 全部历史版本（当前约 299 个，9.7.1 直至 5.0.15） |

两者互补：最新 GA 版本发布初期可能尚未进入归档页，而归档页不含最新版。

### 1.1 版本枚举

**GA 页** `GET https://dev.mysql.com/downloads/mysql/`

页面内 `<select name="version" id="version">` 列出最新版本系列：

```html
<select name="version" size="1" id="version">
    <option value="26.7" selected="selected">26.7.0</option>
    <option value="9.7">9.7.2 LTS</option>
    <option value="8.4">8.4.11 LTS</option>
    <option value="8.0">8.0.46</option>
</select>
```

- `value` 为 `major.minor`（系列号），option 文本为完整补丁版本，可能带 ` LTS` 后缀
- 同页 `<select name="os" id="os">` 列出操作系统及数字 ID，常用的有：
  `3`=Microsoft Windows、`2`=Linux - Generic、`33`=macOS、`src`=Source Code

**归档页** `GET https://downloads.mysql.com/archives/community/`

基础页内 `<select name="version">` 列出**全部历史版本**（完整版本号，如 `9.7.1`、`8.0.35`、`5.7.43`），一次请求即可完成历史版本枚举，无需遍历。

### 1.2 包清单（版本 → 平台 → 安装包）

每个版本 + 操作系统组合可定位到一组具体的安装包：

**GA 页** `GET https://dev.mysql.com/downloads/mysql/?version=<major.minor>&os=<osId>`

例：`?version=8.0&os=33` 返回 8.0 系列最新版的 macOS 安装包。每个安装包由表格中的两行组成：

```html
<tr>
    <td><b>macOS 15 (ARM, 64-bit), Compressed TAR Archive</b></td>  <!-- 描述 -->
    <td>(mysql-8.0.46-macos15-arm64.tar.gz)</td>                    <!-- MD5 行，文件名 -->
    ...
    <td><a href="/downloads/file/?id=551707">Download</a></td>       <!-- 下载端点 -->
    <td>MD5: <code class="md5">aefb85...</code></td>
</tr>
```

关键字段：产品描述（含 OS/架构/包类型）、版本号、大小、`/downloads/file/?id=<id>` 下载端点、文件名、MD5。

**归档页** `GET https://downloads.mysql.com/archives/community/?tpl=version&os=<osId>&version=<完整版本>&osva=`

例：`?os=2&version=9.7.1` 返回 9.7.1 的 Linux - Generic 安装包。**注意 `os` 参数必须显式指定**
（macOS=33、Linux-Generic=2、Windows=3），省略时服务端只渲染默认平台（Windows）的包；
浏览器里看到的完整表格是 JS 按平台逐个异步加载的。下载链接形如：

```
/archives/get/p/<pid>/file/<filename>     相对路径，pid 为产品 ID（MySQL Community Server 固定）
```

## 2. 下载 URL 规则

**首选：CDN 直链拼接（已验证，GA 与历史版本通吃）**

```
https://cdn.mysql.com/Downloads/MySQL-<major.minor>/<filename>
```

例：

```
https://cdn.mysql.com/Downloads/MySQL-8.0/mysql-8.0.46-macos15-arm64.tar.gz
https://cdn.mysql.com/Downloads/MySQL-9.7/mysql-9.7.1-linux-glibc2.28-x86_64.tar.xz
```

**备选（fallback）：**

- GA 端点：`https://dev.mysql.com/downloads/file/?id=<id>`
- 归档端点：`https://downloads.mysql.com/archives/get/p/<pid>/file/<filename>`

## 3. 文件名解析规则

文件名高度规范：`mysql-<version>-<os>[<osver>][-glibc<x>]-<arch>.<ext>`。
dbpod 通过正则 + 关键词表将文件名归一化为内部平台标识：

| 文件名片段 | 归一化 |
| --- | --- |
| `macos15`、`macos14`、`macos13`… | OS=`darwin`，OS 版本=数字 |
| `linux-glibc2.28` 等 | OS=`linux`，记录 glibc 要求 |
| `winx64` | OS=`windows` |
| `arm64` / `aarch64` | Arch=`arm64` |
| `x86_64` | Arch=`amd64` |
| 扩展名 `.tar.gz` / `.tar.xz` / `.zip` / `.tar` / `.dmg` / `.msi` | 包类型 |

包变体（variant）从文件名或描述识别并排除：`-minimal`（最小安装）、
`-test` / `-debug-test`（测试套件）、`-debug`（调试版）、`src`（源码）、`.dmg` / `.msi`（安装器）。

## 4. 元信息机制：生成 → 入库 → 嵌入 → CDN 分发

爬取属于非常规手段，**只在维护者侧、由专门的生成工具执行**，常规 CLI 流程不访问 MySQL 官网页面。元信息采用"生成一次、多处分发"的流水线：

```
cmd/metadata-gen（爬虫，仅维护者运行）
        │  增量抓取官网版本/包清单
        ▼
internal/metadata/data/mysql.json   ←—— 提交进 git 仓库
        │  go:embed                          │  jsdelivr / raw.githubusercontent (CDN)
        ▼                                    ▼
  编译期嵌入二进制        ←—— 兜底 ——   运行时按 24h 规则拉取最新
```

### 4.1 生成工具（维护者）

```bash
go run ./cmd/metadata-gen        # 更新 internal/metadata/data/mysql.json
```

- 增量：文件中已有版本视为不可变，只抓取新增版本的包清单（并发 8）
- 生成后提交仓库；CLI 用户通过 CDN 自动获取，无需升级二进制即可认识新版本

### 4.2 运行时获取链（CLI）

运行时按以下顺序获取元信息，**任何一步都不爬官网**：

1. 本地缓存 `~/.dbpod/metadata/mysql.json`（`DBPOD_HOME` 可重定向），
   `fetched_at` 距今 **不超过 24 小时** 且来源与当前 `--mirror` 一致 → 直接使用
2. 缓存过期/缺失 → 若配置了 mirror 先试 `<mirror>/mysql.json`（无元数据则报错并
   视为不可用），再依次尝试仓库 CDN：
   `https://cdn.jsdelivr.net/gh/shapled/dbpod@main/internal/metadata/data/mysql.json`
   → `https://raw.githubusercontent.com/shapled/dbpod/main/internal/metadata/data/mysql.json`，
   成功后写入本地缓存
3. 网络不可达 → 使用**编译时嵌入二进制**的元数据（`go:embed`），离线可用

### 4.3 文件结构

```json
{
  "engine": "mysql",
  "fetched_at": "2026-08-31T18:30:00+08:00",
  "base_url": "https://cdn.mysql.com/Downloads",
  "versions": {
    "8.0.46": { "series": "8.0", "lts": false, "latest": true,
                 "packages_fetched": true, "packages": [ ... ] },
    "9.7.1":  { "series": "9.7", "packages_fetched": true, "packages": [ ... ] }
  }
}
```

若某版本存在于版本列表但无包清单（理论上不应出现），运行时报错并提示更新 dbpod 或重新生成元信息。

## 5. 镜像源支持

`--mirror <base>`（或 `DBPOD_MIRROR`）指定镜像基地址。**mirror 是完整的元数据+文件源**，
其目录布局与仓库一致：元数据位于 `<base>/mysql.json`，引擎文件位于同一父目录下。

### 5.1 相对 URL 规则

生成的元数据中，包下载 URL 是**相对路径**（相对于元数据文件所在父目录）：

```json
{ "filename": "mysql-8.0.46-macos15-arm64.tar.gz", "url": "MySQL-8.0/mysql-8.0.46-macos15-arm64.tar.gz" }
```

解析规则：

- **相对 URL** → 拼接元数据父目录（`BaseURL`）：
  - 来自官方仓库/内嵌 → `https://cdn.mysql.com/Downloads` + 相对路径
  - 来自 mirror → `<mirror base>` + 相对路径（即 mirror 同时提供元数据与文件）
  - 例：`--mirror https://m.example.com/dbpod`
    → `https://m.example.com/dbpod/MySQL-8.0/mysql-8.0.46-macos15-arm64.tar.gz`
- **绝对 URL**（`http(s)://` 开头）→ 原样使用

### 5.2 mirror 在获取链中的位置

```
24h 内本地缓存 → mirror(<base>/mysql.json) → 仓库 CDN → 二进制内嵌
```

- mirror 配置了但拿不到元数据（404 等）→ **视为该 mirror 不可用**，日志报错，
  继续走仓库 CDN / 内嵌兜底
- 缓存记录了来源 `BaseURL`；更换 `--mirror` 后缓存立即视为过期，重新获取
- 文件 MD5 校验不因 mirror 而改变

## 6. 平台选择策略

`dbpod image pull mysql:<version>` 在目标平台（`runtime.GOOS`/`GOARCH`）上按以下优先级挑选安装包：

1. OS 与 Arch 精确匹配
2. 排除所有非完整包（minimal / test / debug / 源码 / 安装器）
3. 包类型优先级：`tar.gz` > `tar.xz` > `zip`
4. 同类型多个 macOS 版本构建时取最新 OS 版本（如 macos15 优先于 macos14）
