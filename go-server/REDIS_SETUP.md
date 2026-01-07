# Redis集群缓存配置指南

本指南详细说明如何配置和部署Redis集群以替换Go程序的本地磁盘缓存。

## 🏗️ 架构概述

### 缓存架构
```
L1: 内存缓存 (LRU) - 热点数据，超低延迟
    ↓ miss
L2: Redis集群缓存 - 分布式共享，中等延迟  
    ↓ miss
L3: TMDB API请求 - 源数据获取
```

### 地域分布推荐
基于你的部署地域（新加坡、上海、首尔、大阪、阿姆斯特丹），推荐以下Redis集群配置：

```yaml
# 亚太区域集群
Asia-Pacific Cluster:
  - Primary: Singapore (redis.singapore.com:6379)
  - Replicas: 
    - Shanghai (redis.shanghai.com:6379)
    - Seoul (redis.seoul.com:6379)
    - Osaka (redis.osaka.com:6379)

# 欧洲区域集群  
Europe Cluster:
  - Primary: Amsterdam (redis.amsterdam.com:6379)
  - Backup: Singapore (redis.singapore.com:6380)
```

## ⚙️ 环境变量配置

### 基础Redis配置
```bash
# 启用Redis缓存
USE_REDIS=true

# Redis集群节点 (逗号分隔)
REDIS_CLUSTER_NODES=redis.singapore.com:6379,redis.shanghai.com:6379,redis.seoul.com:6379,redis.osaka.com:6379,redis.amsterdam.com:6379

# Redis认证 (如果需要)
REDIS_PASSWORD=your_redis_password

# Redis数据库索引
REDIS_DB=0
```

### 连接池配置
```bash
# 连接池大小
REDIS_POOL_SIZE=10

# 最小空闲连接数
REDIS_MIN_IDLE_CONNS=2

# 连接超时时间 (秒)
REDIS_CONNECT_TIMEOUT=5

# 读取超时时间 (秒)
REDIS_READ_TIMEOUT=3

# 写入超时时间 (秒)
REDIS_WRITE_TIMEOUT=3

# 空闲连接超时时间 (秒)
REDIS_IDLE_TIMEOUT=300

# 最大重试次数
REDIS_MAX_RETRIES=3

# 重试延迟 (毫秒)
REDIS_RETRY_DELAY=500
```

### 优化的TTL配置
```bash
# 基础缓存时间 (分钟)
JSON_MEMORY_TTL=15        # 内存15分钟
JSON_DISK_TTL=360         # Redis 6小时

# 图片缓存时间 (分钟) 
IMAGE_MEMORY_TTL=30       # 内存30分钟
IMAGE_DISK_TTL=10080      # Redis 7天

# 禁用本地缓存
CACHE_ENABLED=true        # 启用缓存功能
```

## 🐳 Docker Compose Redis集群

### redis-cluster.yml
```yaml
version: '3.8'

services:
  redis-singapore:
    image: redis:7-alpine
    container_name: redis-singapore
    ports:
      - "6379:6379"
    volumes:
      - redis-singapore-data:/data
      - ./redis.conf:/usr/local/etc/redis/redis.conf
    command: redis-server /usr/local/etc/redis/redis.conf
    networks:
      - redis-cluster
    deploy:
      resources:
        limits:
          memory: 1G
        reservations:
          memory: 512M

  redis-shanghai:
    image: redis:7-alpine
    container_name: redis-shanghai
    ports:
      - "6380:6379"
    volumes:
      - redis-shanghai-data:/data
      - ./redis.conf:/usr/local/etc/redis/redis.conf
    command: redis-server /usr/local/etc/redis/redis.conf
    networks:
      - redis-cluster

  redis-seoul:
    image: redis:7-alpine
    container_name: redis-seoul
    ports:
      - "6381:6379"
    volumes:
      - redis-seoul-data:/data
      - ./redis.conf:/usr/local/etc/redis/redis.conf
    command: redis-server /usr/local/etc/redis/redis.conf
    networks:
      - redis-cluster

  redis-osaka:
    image: redis:7-alpine
    container_name: redis-osaka
    ports:
      - "6382:6379"
    volumes:
      - redis-osaka-data:/data
      - ./redis.conf:/usr/local/etc/redis/redis.conf
    command: redis-server /usr/local/etc/redis/redis.conf
    networks:
      - redis-cluster

  redis-amsterdam:
    image: redis:7-alpine
    container_name: redis-amsterdam
    ports:
      - "6383:6379"
    volumes:
      - redis-amsterdam-data:/data
      - ./redis.conf:/usr/local/etc/redis/redis.conf
    command: redis-server /usr/local/etc/redis/redis.conf
    networks:
      - redis-cluster

volumes:
  redis-singapore-data:
  redis-shanghai-data:
  redis-seoul-data:
  redis-osaka-data:
  redis-amsterdam-data:

networks:
  redis-cluster:
    driver: bridge
```

### redis.conf 配置文件
```conf
# 基础配置
bind 0.0.0.0
port 6379
timeout 300
keepalive 60

# 内存配置
maxmemory 512mb
maxmemory-policy allkeys-lru

# 持久化配置
save 900 1
save 300 10  
save 60 10000

# 安全配置
# requirepass your_password_here

# 网络配置
tcp-backlog 511
tcp-keepalive 300

# 日志配置
loglevel notice
logfile ""

# 客户端配置
maxclients 10000

# 慢查询配置
slowlog-log-slower-than 10000
slowlog-max-len 128

# 集群配置
cluster-enabled yes
cluster-config-file nodes.conf
cluster-node-timeout 15000
cluster-require-full-coverage no
```

## 🚀 部署步骤

### 1. 启动Redis集群
```bash
# 创建配置文件目录
mkdir -p redis-config

# 复制redis.conf到配置目录
cp redis.conf redis-config/

# 启动Redis集群
docker-compose -f redis-cluster.yml up -d

# 检查集群状态
docker-compose -f redis-cluster.yml ps
```

### 2. 初始化集群
```bash
# 进入任意Redis容器
docker exec -it redis-singapore redis-cli

# 创建集群
redis-cli --cluster create \
  127.0.0.1:6379 \
  127.0.0.1:6380 \
  127.0.0.1:6381 \
  127.0.0.1:6382 \
  127.0.0.1:6383 \
  --cluster-replicas 1
```

### 3. 更新Go程序配置
```bash
# 设置环境变量
export USE_REDIS=true
export REDIS_CLUSTER_NODES=localhost:6379,localhost:6380,localhost:6381,localhost:6382,localhost:6383

# 重启Go程序
./tmdb-go-proxy
```

## 📊 监控和管理

### API端点
```bash
# 查看缓存信息
curl http://localhost:6635/cache/info

# 清除Redis缓存
curl -X POST http://localhost:6635/cache/clear?type=l2

# 查看缓存键
curl http://localhost:6635/cache/keys?limit=10

# 搜索缓存
curl http://localhost:6635/cache/search?q=movie
```

### Redis监控命令
```bash
# 查看集群信息
redis-cli cluster info

# 查看节点状态
redis-cli cluster nodes

# 查看内存使用
redis-cli info memory

# 查看连接数
redis-cli info clients

# 实时监控
redis-cli monitor
```

## 🔧 性能调优

### Redis优化建议
1. **内存优化**
   ```conf
   maxmemory-policy allkeys-lru
   hash-max-ziplist-entries 512
   hash-max-ziplist-value 64
   ```

2. **网络优化**
   ```conf
   tcp-keepalive 300
   timeout 300
   ```

3. **持久化优化**
   ```conf
   # 对于缓存场景，可以禁用持久化
   save ""
   appendonly no
   ```

### Go程序优化
1. **连接池配置**
   ```bash
   REDIS_POOL_SIZE=20
   REDIS_MIN_IDLE_CONNS=5
   ```

2. **超时配置**
   ```bash
   REDIS_READ_TIMEOUT=2
   REDIS_WRITE_TIMEOUT=2
   ```

## 🎯 地域化部署

### 智能路由配置
```bash
# 亚洲服务器配置
REDIS_CLUSTER_NODES=redis.singapore.com:6379,redis.shanghai.com:6379,redis.seoul.com:6379

# 欧洲服务器配置  
REDIS_CLUSTER_NODES=redis.amsterdam.com:6379,redis.singapore.com:6380
```

### 容灾配置
```bash
# 主集群故障时的备用配置
REDIS_FALLBACK_NODES=backup.redis.com:6379

# 启用磁盘缓存作为后备
USE_DISK_CACHE_FALLBACK=true
```

## 🔍 故障排除

### 常见问题

1. **连接失败**
   ```bash
   # 检查网络连通性
   telnet redis.singapore.com 6379
   
   # 检查防火墙设置
   netstat -tuln | grep 6379
   ```

2. **内存不足**
   ```bash
   # 检查内存使用
   redis-cli info memory
   
   # 清理过期键
   redis-cli --scan --pattern "*" | xargs redis-cli del
   ```

3. **集群分裂**
   ```bash
   # 重新加入集群
   redis-cli cluster meet <ip> <port>
   
   # 修复集群
   redis-cli --cluster fix <ip>:<port>
   ```

### 日志分析
```bash
# 查看Go程序日志
grep "Redis" /var/log/tmdb-go-proxy.log

# 查看Redis日志
docker logs redis-singapore
```

## 📈 性能对比

| 缓存类型 | 延迟 | 吞吐量 | 一致性 | 可扩展性 | 成本 |
|---------|------|--------|--------|----------|------|
| 内存缓存 | <1ms | 极高 | 分散 | 低 | 低 |
| Redis集群 | 2-10ms | 高 | 强 | 高 | 中 |
| 磁盘缓存 | 5-50ms | 中 | 分散 | 低 | 极低 |

## 🎉 部署完成验证

部署完成后，访问以下端点验证：

```bash
# 检查Redis缓存状态
curl http://localhost:6635/cache/info

# 发送测试请求
curl http://localhost:6635/movie/popular

# 验证缓存命中
curl http://localhost:6635/movie/popular  # 应该更快
```

成功的响应应该显示：
- `l2_cache.type: "redis"`
- `l2_cache.nodes: [...]`
- 日志中显示"Redis缓存命中"

🎯 **恭喜！Redis集群缓存部署完成！**

现在你的TMDB代理服务已经升级为分布式缓存架构，可以在多个地域间共享缓存数据，大大提升缓存利用率和用户体验！
