#!/bin/bash
# 测试docker-compose配置脚本

set -e

echo "🚀 开始测试Docker Compose配置..."
echo ""

# 停止并删除旧容器
echo "🧹 清理旧容器..."
docker compose -f docker-compose.test.yml down 2>/dev/null || true

# 拉取最新镜像
echo "📥 拉取test标签镜像..."
docker pull ghcr.io/sebastian0619/proxy-nest/proxy-go:test

# 启动服务
echo "▶️  启动服务..."
docker compose -f docker-compose.test.yml up -d

# 等待服务启动
echo "⏳ 等待服务启动..."
sleep 5

# 测试健康检查
echo "🔍 测试健康检查端点..."
for i in {1..10}; do
    if curl -f -s http://localhost:6635/health > /dev/null 2>&1; then
        echo "✅ 健康检查通过！"
        break
    else
        if [ $i -eq 10 ]; then
            echo "❌ 健康检查失败！"
            docker compose -f docker-compose.test.yml logs
            exit 1
        fi
        echo "   等待中... ($i/10)"
        sleep 2
    fi
done

# 测试UI端点
echo "🌐 测试UI端点..."
if curl -f -s http://localhost:6635/ui > /dev/null 2>&1; then
    echo "✅ UI端点可访问！"
else
    echo "⚠️  UI端点测试失败"
fi

# 显示容器状态
echo ""
echo "📊 容器状态:"
docker compose -f docker-compose.test.yml ps

echo ""
echo "✅ 测试完成！"
echo "🌐 UI地址: http://localhost:6635/ui"
echo "🔍 健康检查: http://localhost:6635/health"
echo "📊 状态信息: http://localhost:6635/status"
echo ""
echo "查看日志: docker compose -f docker-compose.test.yml logs -f"
echo "停止服务: docker compose -f docker-compose.test.yml down"
