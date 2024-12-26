# TMDB 图片代理服务器

一个高性能的TMDB图片代理服务器，支持多上游服务器、智能负载均衡、健康检查和缓存系统。

## 主要特点

- **多上游服务器支持**
  - 支持配置多个TMDB图片服务器
  - 自动故障转移
  - 智能负载均衡

- **健康检查系统**
  - 定期检查上游服务器健康状态
  - 自动检测并隔离不健康的服务器
  - 支持服务器恢复检测

- **智能负载均衡**
  - 基于响应时间的动态权重计算
  - 支持基础权重和动态权重
  - 平滑的权重调整算法
  - EWMA（指数加权移动平均）支持

- **高性能缓存系统**
  - 双层缓存架构（内存 + 磁盘）
  - LRU缓存策略
  - 可配置的缓存清理策略
  - 支持不同内容类型的缓存配置

- **多线程处理**
  - Worker线程池处理请求
  - 独立的健康检查线程
  - 优化的线程间通信

- **错误处理和重试**
  - 自动请求重试机制
  - 智能错误检测
  - 优雅的错误恢复

- **完善的日志系统**
  - 彩色日志输出
  - 详细的状态日志
  - 性能监控日志

## 安装

1. 克隆仓库：
```bash
git clone [repository-url]
cd tmdb-image-proxy
```

2. 安装依赖：
```bash
npm install
```

## 配置

1. 环境变量配置（创建 `.env` 文件）：
```env
# 服务器配置
PORT=3000
NUM_WORKERS=4

# 上游服务器
UPSTREAM_SERVERS=https://image.tmdb.org,https://image2.tmdb.org

# TMDB配置
TMDB_API_KEY=your_api_key
UPSTREAM_TYPE=tmdb-image
TMDB_IMAGE_TEST_URL=/t/p/original/wwemzKWzjKYJFfCeiB57q3r4Bcm.png

# 缓存配置
CACHE_DIR=./cache
CACHE_TTL=86400000
MEMORY_CACHE_SIZE=1000
DISK_CACHE_CLEANUP_INTERVAL=3600000

# 健康检查配置
REQUEST_TIMEOUT=5000
UNHEALTHY_TIMEOUT=300000
MAX_ERRORS_BEFORE_UNHEALTHY=3

# 权重配置
BASE_WEIGHT_MULTIPLIER=20
DYNAMIC_WEIGHT_MULTIPLIER=50
ALPHA_INITIAL=0.5
ALPHA_ADJUSTMENT_STEP=0.1
```

2. 配置文件说明：
- `config.js`: 主要配置文件
- `utils.js`: 工具函数和缓存实现
- `worker.js`: 工作线程处理逻辑
- `health_checker.js`: 健康检查实现

## 运行

### 直接运行

```bash
npm start
```

### 使用Docker

1. 构建镜像：
```bash
docker build -t tmdb-image-proxy .
```

2. 运行容器：
```bash
docker run -d \
  -p 3000:3000 \
  -e UPSTREAM_SERVERS=https://image.tmdb.org \
  -e TMDB_API_KEY=your_api_key \
  --name tmdb-proxy \
  tmdb-image-proxy
```

### 使用Docker Compose

```bash
docker-compose up -d
```

## 监控和日志

服务器启动后，可以通过日志查看各项指标：

- 服务器健康状态
- 请求处理情况
- 缓存命中率
- 负载均衡权重分配
- 错误和警告信息

## 性能优化

1. 缓存配置优化：
   - 调整`MEMORY_CACHE_SIZE`和`CACHE_TTL`
   - 配置合适的清理间隔

2. 负载均衡优化：
   - 调整`BASE_WEIGHT_MULTIPLIER`和`DYNAMIC_WEIGHT_MULTIPLIER`
   - 优化`ALPHA_INITIAL`和`ALPHA_ADJUSTMENT_STEP`

3. 健康检查优化：
   - 调整`REQUEST_TIMEOUT`和`UNHEALTHY_TIMEOUT`
   - 配置`MAX_ERRORS_BEFORE_UNHEALTHY`

## 许可证

MIT License
