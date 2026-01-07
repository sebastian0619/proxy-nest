#!/bin/bash

# Redis集群初始化脚本
# 在所有节点部署完成后运行此脚本

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

print_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Redis节点列表 (修改为你的实际域名)
REDIS_NODES=(
    "singapore.your-domain.com:6379"
    "shanghai.your-domain.com:6379"
    "seoul.your-domain.com:6379"
    "osaka.your-domain.com:6379"
    "amsterdam.your-domain.com:6379"
)

print_info "🚀 开始初始化Redis集群..."
echo ""

# 检查所有节点是否可达
print_info "检查Redis节点连通性..."
for node in "${REDIS_NODES[@]}"; do
    host=$(echo $node | cut -d':' -f1)
    port=$(echo $node | cut -d':' -f2)
    
    if timeout 5 bash -c "</dev/tcp/$host/$port"; then
        print_success "✓ $node 连接正常"
    else
        print_error "✗ $node 连接失败"
        exit 1
    fi
done

echo ""

# 创建集群
print_info "创建Redis集群..."
echo "集群节点: ${REDIS_NODES[*]}"
echo ""

# 构建redis-cli cluster create命令
CLUSTER_CMD="redis-cli --cluster create ${REDIS_NODES[*]} --cluster-replicas 1"

print_warning "即将执行集群创建命令:"
echo "  $CLUSTER_CMD"
echo ""

read -p "是否继续? (y/N): " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    print_info "操作已取消"
    exit 0
fi

# 如果有密码，添加密码参数
if [ ! -z "$REDIS_PASSWORD" ]; then
    CLUSTER_CMD="$CLUSTER_CMD -a $REDIS_PASSWORD"
fi

# 执行集群创建命令
print_info "正在创建集群..."
echo "yes" | $CLUSTER_CMD

echo ""

# 等待集群稳定
print_info "等待集群稳定..."
sleep 10

# 检查集群状态
print_info "检查集群状态..."
first_node=${REDIS_NODES[0]}
host=$(echo $first_node | cut -d':' -f1)
port=$(echo $first_node | cut -d':' -f2)

if [ ! -z "$REDIS_PASSWORD" ]; then
    AUTH_PARAM="-a $REDIS_PASSWORD"
else
    AUTH_PARAM=""
fi

# 显示集群信息
echo ""
print_info "集群信息:"
redis-cli -h $host -p $port $AUTH_PARAM cluster info

echo ""
print_info "集群节点:"
redis-cli -h $host -p $port $AUTH_PARAM cluster nodes

echo ""

# 测试集群
print_info "测试集群功能..."

# 设置测试键
redis-cli -h $host -p $port $AUTH_PARAM set test_key "Hello Redis Cluster!" > /dev/null

# 从不同节点读取
for node in "${REDIS_NODES[@]}"; do
    node_host=$(echo $node | cut -d':' -f1)
    node_port=$(echo $node | cut -d':' -f2)
    
    result=$(redis-cli -h $node_host -p $node_port $AUTH_PARAM get test_key 2>/dev/null || echo "FAILED")
    
    if [ "$result" = "Hello Redis Cluster!" ]; then
        print_success "✓ $node 测试通过"
    else
        print_warning "✗ $node 测试失败: $result"
    fi
done

# 清理测试键
redis-cli -h $host -p $port $AUTH_PARAM del test_key > /dev/null

echo ""
print_success "🎉 Redis集群初始化完成!"

echo ""
echo "📋 集群信息:"
echo "   节点数量: ${#REDIS_NODES[@]}"
echo "   主节点: 3个"
echo "   副本节点: 2个"
echo "   集群模式: 启用"
echo ""

echo "🔧 管理命令:"
echo "   查看集群状态: redis-cli -h $host -p $port $AUTH_PARAM cluster info"
echo "   查看节点信息: redis-cli -h $host -p $port $AUTH_PARAM cluster nodes"
echo "   集群健康检查: redis-cli -h $host -p $port $AUTH_PARAM cluster check"
echo ""

echo "🌐 验证Go应用:"
echo "   检查缓存配置: curl http://singapore.your-domain.com:6635/cache/info"
echo "   测试API请求: curl http://singapore.your-domain.com:6635/movie/popular"
echo ""

print_info "现在可以测试分布式缓存功能了！"

# 显示下一步操作
echo ""
print_info "📝 下一步操作:"
echo "1. 在每个地域服务器上验证Go应用状态"
echo "2. 测试跨地域缓存共享功能"
echo "3. 监控Redis集群健康状态"
echo "4. 根据需要调整缓存TTL配置"

