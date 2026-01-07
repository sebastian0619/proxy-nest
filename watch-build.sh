#!/bin/bash

# GitHub Actions 构建监控脚本
# 使用方法: ./watch-build.sh

REPO="sebastian0619/proxy-nest"

echo "🔍 GitHub Actions 构建监控"
echo "=========================="
echo ""

# 检查是否安装了 GitHub CLI
if command -v gh &> /dev/null; then
    echo "✅ 检测到 GitHub CLI，使用 gh 命令查看..."
    echo ""
    gh run list --limit 5
    echo ""
    echo "查看最新构建日志:"
    gh run watch
else
    echo "⚠️  未安装 GitHub CLI"
    echo ""
    echo "📦 安装 GitHub CLI (macOS):"
    echo "   brew install gh"
    echo ""
    echo "🔐 登录 GitHub:"
    echo "   gh auth login"
    echo ""
    echo "📊 查看构建状态:"
    echo "   gh run list"
    echo "   gh run watch"
    echo ""
    echo "🌐 或直接在浏览器中查看:"
    echo "   https://github.com/${REPO}/actions"
    echo ""
    
    # 使用 API 查看状态
    echo "当前构建状态（通过 API）:"
    curl -s "https://api.github.com/repos/${REPO}/actions/runs?per_page=1" | \
        python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
    r = d['workflow_runs'][0] if d.get('workflow_runs') else None
    if r:
        status = r.get('status', 'unknown')
        conclusion = r.get('conclusion', '进行中')
        url = r.get('html_url', '')
        name = r.get('name', 'N/A')
        sha = r.get('head_sha', '')[:7]
        
        print(f'   工作流: {name}')
        print(f'   提交: {sha}')
        print(f'   状态: {status}')
        print(f'   结果: {conclusion}')
        print(f'   链接: {url}')
        
        if conclusion == 'failure':
            print('')
            print('   ❌ 构建失败！请查看上面的链接获取详细错误信息')
    else:
        print('   无法获取构建信息')
except Exception as e:
    print(f'   错误: {e}')
" 2>/dev/null || echo "   需要 Python 3 来解析 JSON"
fi

