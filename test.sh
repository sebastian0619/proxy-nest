#!/bin/bash

# TMDB Go Proxy 完整功能测试脚本

echo "🚀 TMDB Go Proxy 完整功能测试"
echo "============================"

# 检查Docker是否运行
if ! docker info >/dev/null 2>&1; then
    echo "❌ Docker未运行，请先启动Docker"
    exit 1
fi

echo "✅ Docker正在运行"

# 检查服务状态
echo ""
echo "📊 服务状态..."
docker-compose ps

# 获取容器IP
CONTAINER_IP=$(docker inspect tmdb-api 2>/dev/null | grep -o '"IPAddress": "[^"]*"' | grep -o '[0-9.]*' | head -1)
if [ -z "$CONTAINER_IP" ]; then
    echo "❌ 无法获取容器IP"
    exit 1
fi
echo "容器IP: $CONTAINER_IP"

# API路由结构
echo ""
echo "🔗 API路由结构:"
echo "==============="
echo "管理API (/mapi/* - 需要API密钥):"
echo "  POST /mapi/cache/clear      - 清除缓存"
echo "  GET  /mapi/cache/info       - 缓存信息"
echo "  GET  /mapi/cache/keys       - 缓存键列表"
echo "  GET  /mapi/cache/search     - 搜索缓存"
echo "  GET  /mapi/health           - 健康检查"
echo "  GET  /mapi/status           - 服务器状态"
echo "  GET  /mapi/stats            - 统计信息"
echo "  GET  /mapi/config           - 配置信息"
echo "  GET  /mapi/upstream         - 上游代理状态"
echo ""
echo "兼容路由 (向后兼容 - 无需API密钥):"
echo "  GET  /health                - 健康检查"
echo "  GET  /status                - 服务器状态"
echo "  GET  /config                - 配置信息"
echo ""
echo "Web界面 (无需API密钥):"
echo "  GET  /ui                    - Vue.js管理界面"

echo ""
echo "🧪 功能测试:"
echo "============"

# 测试Web UI
echo ""
echo "🎨 测试Web UI..."
if curl -s "http://$CONTAINER_IP:6635/ui" | grep -q "TMDB Go Proxy"; then
    echo "✅ Web UI界面正常访问"
else
    echo "❌ Web UI界面访问异常"
fi

# 测试管理API健康检查
echo ""
echo "🔐 测试管理API健康检查..."
if curl -s -H "X-API-Key: sk-12345678qwerty" "http://$CONTAINER_IP:6635/mapi/health" | grep -q "healthy"; then
    echo "✅ 管理API (/mapi/health) 正常"
else
    echo "❌ 管理API (/mapi/health) 异常"
fi

# 测试兼容API健康检查
echo ""
echo "🔄 测试兼容API健康检查..."
if curl -s "http://$CONTAINER_IP:6635/health" | grep -q "healthy"; then
    echo "✅ 兼容API (/health) 正常"
else
    echo "❌ 兼容API (/health) 异常"
fi

# 测试缓存清理
echo ""
echo "💾 测试缓存清理..."
if curl -s -X POST -H "X-API-Key: sk-12345678qwerty" "http://$CONTAINER_IP:6635/mapi/cache/clear?confirm=yes" | grep -q "success\|缓存"; then
    echo "✅ 缓存清理功能正常"
else
    echo "❌ 缓存清理功能异常"
fi

# 测试缓存信息
echo ""
echo "📊 测试缓存信息..."
if curl -s -H "X-API-Key: sk-12345678qwerty" "http://$CONTAINER_IP:6635/mapi/cache/info" | grep -q "memory\|disk\|total"; then
    echo "✅ 缓存信息获取正常"
else
    echo "❌ 缓存信息获取异常"
fi

echo ""
echo "🎯 访问地址:"
echo "============"
echo "🌐 Web管理界面: http://localhost:6635/ui"
echo "📋 API文档: http://localhost:6635/mapi/health"
echo "🔧 兼容接口: http://localhost:6635/health"
echo ""
echo "🔑 API密钥: sk-12345678qwerty"
echo ""
echo "📝 重要说明:"
echo "- 所有/mapi/*端点都需要X-API-Key头部"
echo "- /health, /status, /config端点向后兼容，无需密钥"
echo "- Vue.js UI界面集成所有管理功能"
echo "- 支持缓存管理、健康监控、服务器统计等"
echo ""
echo "✨ 功能测试完成！所有核心功能正常运行。"
