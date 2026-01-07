#!/bin/bash

# TMDB Go Proxy Redis集群部署脚本
# 使用方法: ./deploy-redis-cluster.sh [location]
# 例如: ./deploy-redis-cluster.sh singapore

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 打印彩色信息
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

# 检查参数
if [ -z "$1" ]; then
    print_error "请指定部署位置！"
    echo "使用方法: $0 [singapore|shanghai|seoul|osaka|amsterdam]"
    exit 1
fi

LOCATION=$1
COMPOSE_FILE="docker-compose.${LOCATION}.yml"

# 验证位置
case $LOCATION in
    singapore|shanghai|seoul|osaka|amsterdam)
        print_info "部署位置: ${LOCATION}"
        ;;
    *)
        print_error "无效的位置: ${LOCATION}"
        echo "支持的位置: singapore, shanghai, seoul, osaka, amsterdam"
        exit 1
        ;;
esac

# 检查必要文件
print_info "检查必要文件..."

required_files=(
    "$COMPOSE_FILE"
    "redis.conf"
    "Dockerfile"
)

for file in "${required_files[@]}"; do
    if [ ! -f "$file" ]; then
        print_error "缺少必要文件: $file"
        exit 1
    fi
done

print_success "所有必要文件检查通过"

# 检查环境变量
print_info "检查环境变量..."

if [ -z "$TMDB_API_KEY" ]; then
    print_warning "未设置 TMDB_API_KEY 环境变量"
    read -p "请输入TMDB API Key: " TMDB_API_KEY
    export TMDB_API_KEY
fi

if [ -z "$REDIS_PASSWORD" ]; then
    print_warning "未设置 REDIS_PASSWORD，使用默认配置"
    export REDIS_PASSWORD=""
fi

print_success "环境变量检查完成"

# 停止现有服务
print_info "停止现有服务..."
docker-compose -f "$COMPOSE_FILE" down --remove-orphans || true

# 清理旧的镜像 (可选)
if [ "$2" = "--clean" ]; then
    print_info "清理旧镜像..."
    docker system prune -f
fi

# 构建镜像
print_info "构建Go应用镜像..."
docker-compose -f "$COMPOSE_FILE" build --no-cache

# 启动服务
print_info "启动Redis集群节点..."
docker-compose -f "$COMPOSE_FILE" up -d

# 等待服务启动
print_info "等待服务启动..."
sleep 10

# 检查服务状态
print_info "检查服务状态..."
docker-compose -f "$COMPOSE_FILE" ps

# 检查Redis连接
print_info "检查Redis连接..."
redis_container="redis-${LOCATION}"

if docker exec "$redis_container" redis-cli ping > /dev/null 2>&1; then
    print_success "Redis连接正常"
else
    print_error "Redis连接失败"
    exit 1
fi

# 检查Go应用
print_info "检查Go应用健康状态..."
sleep 5

if curl -f http://localhost:6635/health > /dev/null 2>&1; then
    print_success "Go应用健康检查通过"
else
    print_warning "Go应用健康检查失败，检查日志"
    docker-compose -f "$COMPOSE_FILE" logs tmdb-go-proxy
fi

# 显示Redis信息
print_info "Redis节点信息:"
docker exec "$redis_container" redis-cli info server | grep redis_version
docker exec "$redis_container" redis-cli info memory | grep used_memory_human

# 显示缓存信息
print_info "检查缓存配置..."
curl -s http://localhost:6635/cache/info | jq '.' || echo "缓存信息获取失败"

print_success "🎉 ${LOCATION} 节点部署完成!"

echo ""
echo "📋 部署总结:"
echo "   位置: ${LOCATION}"
echo "   Go应用端口: 6635"
echo "   Redis端口: 6379"
echo "   缓存类型: Redis集群"
echo ""
echo "🔧 管理命令:"
echo "   查看服务状态: docker-compose -f ${COMPOSE_FILE} ps"
echo "   查看日志: docker-compose -f ${COMPOSE_FILE} logs -f"
echo "   重启服务: docker-compose -f ${COMPOSE_FILE} restart"
echo "   停止服务: docker-compose -f ${COMPOSE_FILE} down"
echo ""
echo "🌐 API端点:"
echo "   健康检查: http://localhost:6635/health"
echo "   缓存信息: http://localhost:6635/cache/info"
echo "   服务状态: http://localhost:6635/status"
echo ""

if [ "$LOCATION" = "singapore" ]; then
    echo "🎛️  Redis管理界面: http://localhost:8081"
    echo ""
fi

print_info "如需初始化Redis集群，请在所有节点部署完成后运行:"
echo "   ./init-redis-cluster.sh"

