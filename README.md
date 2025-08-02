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

### 2. ⚖️ 负载均衡
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

## �� 许可证

MIT License
