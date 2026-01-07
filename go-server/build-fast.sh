#!/bin/bash

# 快速构建Go服务镜像脚本
set -e

# 配置
IMAGE_NAME="proxy-go"
TAG=${1:-latest}
PLATFORM=${2:-linux/amd64}

echo "🚀 开始快速构建 Go 服务镜像..."
echo "📦 镜像名称: $IMAGE_NAME"
echo "🏷️  标签: $TAG"
echo "🖥️  平台: $PLATFORM"

# 检查Docker是否运行
if ! docker info > /dev/null 2>&1; then
    echo "❌ Docker 未运行，请先启动 Docker"
    exit 1
fi

# 构建镜像
echo "🔨 构建镜像..."
docker buildx build \
    --platform $PLATFORM \
    --tag $IMAGE_NAME:$TAG \
    --cache-from type=local,src=/tmp/.buildx-cache \
    --cache-to type=local,dest=/tmp/.buildx-cache-new,mode=max \
    --load \
    .

# 移动缓存
rm -rf /tmp/.buildx-cache
mv /tmp/.buildx-cache-new /tmp/.buildx-cache

echo "✅ 构建完成！"
echo "📊 镜像信息:"
docker images $IMAGE_NAME:$TAG

# 可选：运行测试
if [ "$3" = "--test" ]; then
    echo "🧪 运行容器测试..."
    docker run --rm -d --name test-$IMAGE_NAME -p 6635:6635 $IMAGE_NAME:$TAG
    
    # 等待服务启动
    sleep 3
    
    # 测试健康检查
    if curl -f http://localhost:6635/health > /dev/null 2>&1; then
        echo "✅ 服务启动成功！"
    else
        echo "❌ 服务启动失败！"
    fi
    
    # 停止测试容器
    docker stop test-$IMAGE_NAME
fi

echo "🎉 快速构建完成！" 