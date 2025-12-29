#!/bin/bash

# 测试Docker镜像脚本
set -e

# 配置
REGISTRY="ghcr.io"
REPO="${GITHUB_REPOSITORY:-your-username/tmdb-go-proxy}"
IMAGE_NAME="${REGISTRY}/${REPO}/proxy-go"
TAG="${1:-test}"
PORT="${2:-6635}"

echo "🚀 开始测试Docker镜像..."
echo "📦 镜像: ${IMAGE_NAME}:${TAG}"
echo "🔌 端口: ${PORT}"

# 检查Docker是否运行
if ! docker info > /dev/null 2>&1; then
    echo "❌ Docker 未运行，请先启动 Docker"
    exit 1
fi

# 停止并删除旧容器（如果存在）
echo "🧹 清理旧容器..."
docker stop tmdb-api-test 2>/dev/null || true
docker rm tmdb-api-test 2>/dev/null || true

# 拉取镜像
echo "📥 拉取镜像 ${IMAGE_NAME}:${TAG}..."
docker pull "${IMAGE_NAME}:${TAG}"

# 运行容器
echo "▶️  启动容器..."
docker run -d \
    --name tmdb-api-test \
    --rm \
    -p "${PORT}:6635" \
    -e PORT=6635 \
    -e UPSTREAM_TYPE=tmdb-api \
    -e UPSTREAM_SERVERS="http://134.185.84.215:6635,http://129.150.46.127:6635,https://api.themoviedb.org" \
    -e CACHE_ENABLED=true \
    -e LOG_LEVEL=info \
    "${IMAGE_NAME}:${TAG}"

# 等待服务启动
echo "⏳ 等待服务启动..."
sleep 5

# 测试健康检查
echo "🔍 测试健康检查端点..."
if curl -f -s "http://localhost:${PORT}/health" > /dev/null 2>&1; then
    echo "✅ 健康检查通过！"
else
    echo "❌ 健康检查失败！"
    docker logs tmdb-api-test --tail 20
    exit 1
fi

# 测试UI端点
echo "🌐 测试UI端点..."
if curl -f -s "http://localhost:${PORT}/ui" > /dev/null 2>&1; then
    echo "✅ UI端点可访问！"
    echo "   访问地址: http://localhost:${PORT}/ui"
else
    echo "⚠️  UI端点测试失败（可能正常，取决于配置）"
fi

# 显示容器状态
echo ""
echo "📊 容器状态:"
docker ps | grep tmdb-api-test

# 显示日志（最后20行）
echo ""
echo "📋 最近日志:"
docker logs tmdb-api-test --tail 20

echo ""
echo "✅ 测试完成！"
echo "🌐 UI地址: http://localhost:${PORT}/ui"
echo "🔍 健康检查: http://localhost:${PORT}/health"
echo "📊 状态信息: http://localhost:${PORT}/status"
echo ""
echo "停止容器: docker stop tmdb-api-test"
echo "查看日志: docker logs -f tmdb-api-test"

