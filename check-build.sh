#!/bin/bash

# GitHub Actions 构建状态检查脚本

REPO="sebastian0619/proxy-nest"
API_URL="https://api.github.com/repos/${REPO}/actions/runs"

echo "🔍 检查 GitHub Actions 构建状态..."
echo "=================================="
echo ""

# 获取最新的构建运行
response=$(curl -s "${API_URL}?per_page=3")

# 使用 Python 解析 JSON（如果可用）
if command -v python3 &> /dev/null; then
    echo "$response" | python3 -c "
import json
import sys
from datetime import datetime

try:
    data = json.load(sys.stdin)
    runs = data.get('workflow_runs', [])
    
    if not runs:
        print('❌ 没有找到构建记录')
        sys.exit(1)
    
    for i, run in enumerate(runs[:3], 1):
        print(f'\n📦 构建 #{i}')
        print(f'   工作流: {run.get(\"name\", \"N/A\")}')
        print(f'   分支: {run.get(\"head_branch\", \"N/A\")}')
        print(f'   提交: {run.get(\"head_sha\", \"N/A\")[:7]}')
        print(f'   状态: {run.get(\"status\", \"N/A\")}')
        print(f'   结果: {run.get(\"conclusion\", \"进行中...\")}')
        
        created = run.get('created_at', '')
        if created:
            print(f'   创建时间: {created}')
        
        html_url = run.get('html_url', '')
        if html_url:
            print(f'   链接: {html_url}')
        print('   ---')
    
    # 显示最新构建的详细状态
    latest = runs[0]
    status = latest.get('status', 'unknown')
    conclusion = latest.get('conclusion', '')
    
    print(f'\n📊 最新构建状态:')
    if status == 'completed':
        if conclusion == 'success':
            print('   ✅ 构建成功！')
        elif conclusion == 'failure':
            print('   ❌ 构建失败')
        else:
            print(f'   ⚠️  构建完成，结果: {conclusion}')
    elif status == 'in_progress':
        print('   🔄 构建进行中...')
    elif status == 'queued':
        print('   ⏳ 构建排队中...')
    else:
        print(f'   ❓ 状态: {status}')
    
    print(f'\n🌐 在浏览器中查看: https://github.com/${REPO}/actions')
    
except Exception as e:
    print(f'❌ 解析错误: {e}')
    sys.exit(1)
" 2>/dev/null

    if [ $? -eq 0 ]; then
        exit 0
    fi
fi

# 如果没有 Python，使用简单的 grep 解析
echo "使用简单模式解析..."
echo "$response" | grep -o '"name":"[^"]*"' | head -3 | sed 's/"name":"\(.*\)"/工作流: \1/'
echo "$response" | grep -o '"status":"[^"]*"' | head -1 | sed 's/"status":"\(.*\)"/状态: \1/'
echo "$response" | grep -o '"conclusion":"[^"]*"' | head -1 | sed 's/"conclusion":"\(.*\)"/结果: \1/' || echo "结果: 进行中..."
echo ""
echo "🌐 在浏览器中查看: https://github.com/${REPO}/actions"
