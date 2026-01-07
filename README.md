# 🎬 TMDB 代理服务器

一个高性能的代理服务器，支持TMDB API代理、TMDB图片代理以及自定义上游服务器。具备智能负载均衡、健康检查和缓存系统。

## ✨ 功能说明

### 1. 🔄 代理功能
- **TMDB API代理**
  - 🚀 支持所有TMDB API v3接口
  - 🔑 自动处理API密钥认证
  - 📝 支持查询参数透传
  - 🔄 保持原始响应格式
  - 💾 API响应数据缓存

- **TMDB 图片代理**
  - 🖼️ 支持所有TMDB图片尺寸和格式
  - 🎭 支持海报、背景、人物图片等
  - 🔄 自动处理图片格式转换
  - 💯 保持原始图片质量
  - 📦 高效的图片缓存

- **自定义上游代理**
  - 🌐 支持任意HTTP/HTTPS上游服务器
  - ⚙️ 可配置的内容类型验证
  - 🔍 自定义健康检查路径
  - 🔀 灵活的请求转发规则
  - 🛠️ 支持自定义响应处理

### 2. 🔒 API安全保护

Go版本新增API安全保护机制：

- **API密钥验证**: 敏感操作需要API密钥
- **白名单机制**: 健康检查等端点无需验证
- **审计日志**: 记录敏感操作的访问信息
- **请求验证**: 防止无效的缓存清理请求

**敏感端点保护**:
- `/cache/clear` - 缓存清理 (高危)
- `/cache/keys` - 缓存键列表 (中危)
- `/config` - 配置信息 (中危)

**白名单端点** (无需API密钥):
- `/health` - 健康检查
- `/status` - 服务器状态
- `/stats` - 统计信息
- `/upstream` - 上游代理状态

### 3. ⚖️ 负载均衡
- **动态权重计算**
  - ⏱️ 基于响应时间的实时权重调整
  - 📊 支持基础权重（长期表现）
  - 📈 支持动态权重（短期响应）
  - 🔄 平滑的权重过渡
  - 📉 EWMA算法避免权重剧烈波动

- **智能服务器选择**
  - 🎯 按权重概率选择服务器
  - ❌ 自动跳过不健康的服务器
  - 🌡️ 支持服务器预热期
  - 🛡️ 防止单服务器过载
  - 🔄 自动故障转移

### 3. 🏥 健康检查
- **主动健康检查**
  - ⏰ 定期检查所有上游服务器
  - ⚙️ 支持自定义检查间隔
  - 🎯 类型特定的检查策略
  - 🔄 自动恢复检测
  - 📢 健康状态实时广播

- **被动健康检查**
  - 📝 请求失败计数
  - 🔒 自动隔离不健康服务器
  - ⚙️ 可配置的错误阈值
  - 🔄 支持临时故障恢复
  - 🛡️ 防止恢复期间过载

### 4. 💾 缓存系统
- **双层缓存架构**
  - 💨 内存缓存（快速访问）
  - 💿 磁盘缓存（持久存储）
  - 🔄 LRU淘汰策略
  - 📊 支持不同内容类型的缓存策略
  - 🧹 缓存自动清理

- **缓存优化**
  - 🔑 智能的缓存键生成
  - ⏱️ 可配置的缓存有效期
  - 📝 支持查询参数缓存
  - 🔥 缓存预热机制
  - 📊 缓存命中率统计

### 5. ⚠️ 错误处理
- **自动重试机制**
  - 🔄 可配置的重试次数
  - ⏱️ 智能的重试延迟
  - 🔍 区分临时和永久错误
  - 🛡️ 避免重试风暴
  - 📝 详细的错误日志

- **错误恢复**
  - 🛠️ 优雅的错误处理
  - 📉 自动服务降级
  - ⏱️ 请求超时保护
  - 🚦 并发请求限制
  - 🔌 熔断器机制

### 6. 🚀 性能优化
- **多线程处理**
  - 👥 Worker线程池
  - 🏥 独立的健康检查线程
  - 📡 优化的线程通信
  - ⚖️ 自动负载均衡
  - 💻 CPU亲和性支持

- **资源管理**
  - 💾 内存使用优化
  - 🔌 连接池管理
  - 📊 请求队列控制
  - 💿 磁盘空间管理
  - 📈 系统资源监控

## 🔧 部署运行

### 🐳 使用Docker Compose（推荐）

> **🚀 多架构支持**: 本项目支持多种CPU架构，包括：
> - **linux/amd64**: Intel/AMD x86_64 处理器
> - **linux/arm64**: ARM 64位处理器（Apple Silicon M1/M2、树莓派4等）
> - **linux/arm/v7**: ARM 32位 v7处理器（树莓派3等）
>
> Docker会自动选择适合您系统的镜像版本。

1. 创建`docker-compose.yml`：
```yaml
version: '3'

services:
  tmdb-proxy:
    image: ghcr.io/sebastian0619/proxy-nest/proxy-js:latest
    container_name: tmdb-proxy
    ports:
      - "6635:6635"
    environment:
      # 服务器配置
      - PORT=6635
      - NUM_WORKERS=4                # 工作线程数，建议设置为CPU核心数-1
      - TZ=Asia/Shanghai            # 时区
      
      # TMDB配置
      - UPSTREAM_TYPE=tmdb-api       # 上游服务器类型：tmdb-api 或 tmdb-image
      - TMDB_API_KEY=your_api_key    # 替换为你的TMDB API密钥
      - TMDB_IMAGE_TEST_URL=/t/p/original/wwemzKWzjKYJFfCeiB57q3r4Bcm.png
      
      # 上游服务器列表
      - UPSTREAM_SERVERS=https://api.themoviedb.org,https://api.tmdb.org
      
      # 缓存配置
      - CACHE_DIR=/app/cache
      - CACHE_MAX_SIZE=1000          # 磁盘缓存最大条目数
      - CACHE_ENABLED=true           # 是否启用本地缓存，设置为false可完全禁用缓存功能
      
      # 内存缓存配置
      - MEMORY_CACHE_SIZE=1000       # 内存缓存条目数
      - MEMORY_CACHE_TTL=3600000     # 内存缓存过期时间（1小时）
      - MEMORY_CACHE_CLEANUP_INTERVAL=60000  # 内存缓存清理间隔（1分钟）
      
      # 磁盘缓存配置
      - DISK_CACHE_TTL=86400000      # 磁盘缓存过期时间（24小时）
      - DISK_CACHE_CLEANUP_INTERVAL=3600000  # 磁盘缓存清理间隔（1小时）
      
      # 健康检查配置
      - REQUEST_TIMEOUT=5000        # 请求超时时间（5秒）
      - UNHEALTHY_TIMEOUT=663500    # 不健康状态超时（5分钟）
      - MAX_ERRORS_BEFORE_UNHEALTHY=3
      - HEALTH_CHECK_INTERVAL=66350 # 健康检查间隔（30秒）
      
      # 负载均衡配置
      - BASE_WEIGHT_MULTIPLIER=20    # 基础权重乘数
      - DYNAMIC_WEIGHT_MULTIPLIER=50 # 动态权重乘数
      - ALPHA_INITIAL=0.5           # 初始alpha值
      - ALPHA_ADJUSTMENT_STEP=0.1   # alpha调整步长
      - MAX_SERVER_SWITCHES=3       # 最大服务器切换次数
    volumes:
      - ./cache:/app/cache
    restart: unless-stopped


2. 启动服务：
```bash
docker-compose up -d
```

## 📝 使用示例

### 🎬 TMDB API 代理
```bash
# 获取电影详情
curl http://localhost:6635/3/movie/550

# 搜索电影
curl http://localhost:6635/3/search/movie?query=inception
```

### 🖼️ TMDB 图片代理
```bash
# 获取海报图片
curl http://localhost:6635/t/p/original/wwemzKWzjKYJFfCeiB57q3r4Bcm.png

# 获取背景图片
curl http://localhost:6635/t/p/original/wwemzKWzjKYJFfCeiB57q3r4Bcm.jpg
```

### 🌐 自定义上游
```bash
# 代理任意请求
curl http://localhost:6635/your/custom/path
```

## 📊 监控和日志

查看容器日志：
```bash
# 实时查看日志
docker logs -f tmdb-proxy

# 查看最近100条日志
docker logs --tail 100 tmdb-proxy
```

## ⚡ 性能优化建议

1. 💾 缓存配置优化：
   - 根据上游类型调整`CACHE_TTL`（API: 1小时, 图片: 24小时）
   - 调整`MEMORY_CACHE_SIZE`（建议: 1000-5000）
   - 配置合适的清理间隔（建议: 1小时）

2. ⚖️ 负载均衡优化：
   - 调整`BASE_WEIGHT_MULTIPLIER`（建议: 20-50）
   - 调整`DYNAMIC_WEIGHT_MULTIPLIER`（建议: 50-100）

3. 🏥 健康检查优化：
   - 调整`REQUEST_TIMEOUT`（建议: 5000ms）
   - 调整`UNHEALTHY_TIMEOUT`（建议: 5分钟）
   - 调整`MAX_ERRORS_BEFORE_UNHEALTHY`（建议: 3-5）

## 🔒 API安全保护

Go版本实现了全面的API安全保护机制，防止未经授权的访问和操作。

### 安全特性

- **API密钥验证**: 敏感端点需要有效的API密钥
- **白名单机制**: 健康检查等监控端点无需验证
- **请求验证**: 防止无效参数的缓存清理操作
- **审计日志**: 记录所有敏感操作的访问信息

### 配置API密钥

```bash
# 1. 设置环境变量 (强烈推荐)
export API_KEY=your_secure_api_key_here

# 2. 启动程序
docker-compose up
```

### 端点安全等级

| 端点 | 风险等级 | 保护要求 | 说明 |
|------|----------|----------|------|
| `/cache/clear` | 🔴 高危 | API密钥+确认 | 清除所有缓存 |
| `/cache/keys` | 🟡 中危 | API密钥 | 暴露缓存键信息 |
| `/config` | 🟡 中危 | API密钥 | 暴露服务器配置 |
| `/cache/info` | ⚠️ 低危 | API密钥 | 缓存统计信息 |
| `/cache/search` | ⚠️ 低危 | API密钥 | 缓存搜索功能 |
| `/health` | ✅ 安全 | 无需密钥 | 健康检查 |
| `/status` | ✅ 安全 | 无需密钥 | 服务器状态 |
| `/stats` | ✅ 安全 | 无需密钥 | 统计信息 |
| `/upstream` | ✅ 安全 | 无需密钥 | 上游代理状态 |

### 使用示例

```bash
# ✅ 健康检查 - 无需API密钥
curl http://localhost:6635/health

# ✅ 服务器状态 - 无需API密钥
curl http://localhost:6635/status

# ❌ 缓存清理 - 需要API密钥
curl -X POST http://localhost:6635/cache/clear
# 返回: {"error": "API key required"}

# ✅ 缓存清理 - 使用API密钥
curl -X POST http://localhost:6635/cache/clear?confirm=yes \
  -H "X-API-Key: your_secure_api_key_here"

# ✅ 缓存清理 - 清除特定类型
curl -X POST http://localhost:6635/cache/clear?type=memory \
  -H "X-API-Key: your_secure_api_key_here"
```

### 安全建议

1. **生产环境必须设置API_KEY**
2. **定期更换API密钥**
3. **监控敏感端点的访问日志**
4. **使用强密码作为API密钥**
5. **限制API密钥的网络访问范围**

## 🔗 嵌套代理检测

### 功能说明
Go版本支持检测上游服务器是否也是基于相同程序部署的代理服务器。当检测到嵌套代理时，可以实现缓存清理的联动功能。

### 工作原理
1. **程序标识**: 所有响应都包含 `X-TMDB-Proxy` 和 `X-TMDB-Proxy-Version` 头信息
2. **自动检测**: 代理请求时自动检测上游服务器的响应头
3. **联动清理**: 清理本地缓存时自动调用上游代理的缓存清理API

### 配置选项
```bash
# 启用嵌套代理检测 (默认: true)
export ENABLE_NESTED_PROXY_DETECTION=true
```

### 查看上游代理状态
```bash
curl http://localhost:6635/upstream
```

响应示例:
```json
{
  "enabled": true,
  "total_upstream_servers": 2,
  "tmdb_proxy_servers": 1,
  "upstream_servers": [
    {
      "url": "https://api.example.com",
      "is_tmdb_proxy": true,
      "version": "1.0",
      "last_checked": "2025-11-20T10:30:00Z",
      "check_count": 15
    },
    {
      "url": "https://another-api.com",
      "is_tmdb_proxy": false,
      "version": "",
      "last_checked": "2025-11-20T10:25:00Z",
      "check_count": 8
    }
  ]
}
```

### 联动缓存清理
当启用嵌套代理检测时，执行本地缓存清理会自动触发上游TMDB代理的缓存清理:

```bash
# 清理所有缓存 (包括上游代理)
curl -X POST http://localhost:6635/cache/clear
```

日志输出示例:
```
检测到上游服务器 https://api.example.com 也是TMDB代理程序 (版本: 1.0)
开始执行上游代理缓存清理联动 (类型: all)
调用上游代理缓存清理API: https://api.example.com/cache/clear?type=all
成功清理上游代理 https://api.example.com 的缓存 (类型: all)
```

## �� 许可证

MIT License
