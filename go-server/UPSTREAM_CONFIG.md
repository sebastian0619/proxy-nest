# 上下游服务器配置指南

## 概述

本文档说明如何配置上下游服务器，使其能够相互检测和调度。

## 1. 自动检测机制（无需配置）

### 工作原理

系统会在**所有HTTP响应**中自动添加以下响应头：
- `X-TMDB-Proxy: tmdb-go-proxy/1.0`
- `X-TMDB-Proxy-Version: 1.0`

下游服务器会自动检测这些响应头，识别上游服务器是否为TMDB代理。

### 要求

- ✅ **无需配置任何环境变量**
- ✅ 只要服务器运行此版本代码，就会自动添加响应头
- ✅ 下游服务器会自动检测并记录上游代理服务器

### 示例

当上游服务器（Server A）处理请求时，响应头会自动包含：
```
X-TMDB-Proxy: tmdb-go-proxy/1.0
X-TMDB-Proxy-Version: 1.0
```

下游服务器（Server B）收到响应后，会自动检测并记录 Server A 为上游代理。

## 2. 手动配置上游服务器（可选）

如果自动检测失败（例如上游服务器还未更新到此版本），可以手动配置：

### 环境变量

```bash
export UPSTREAM_PROXY_SERVERS=http://server1:6635,http://server2:6635,http://server3:6635
```

### 说明

- 多个服务器用逗号分隔
- 支持 `http://` 和 `https://`
- 手动配置的服务器会显示在 `/api/upstream` 端点中
- 手动配置主要用于聚合API（`/mapi/upstream/aggregate`）

## 3. API Key 配置（可选但推荐）

### 作用

`API_KEY` 用于保护管理API端点（`/mapi/*`），防止未授权访问。

### 配置方法

```bash
export API_KEY=your_secure_api_key_here
```

### 重要说明

⚠️ **上下游服务器的 API_KEY 不需要相同！**

- 每个服务器有自己独立的 `API_KEY`
- `API_KEY` 只用于保护**自己的**管理API
- 当上游服务器调用下游服务器的管理API时：
  - 上游服务器会传递自己的 `API_KEY`（如果设置了）
  - 下游服务器会验证这个 `API_KEY` 是否匹配自己配置的 `API_KEY`
  - 如果匹配，允许访问；如果不匹配，返回401错误

### 使用场景

#### 场景1：不设置 API_KEY（开发环境）

```bash
# 上游服务器
# 不设置 API_KEY

# 下游服务器
# 不设置 API_KEY

# 结果：所有API都可以直接访问，会有警告日志
```

#### 场景2：只设置下游服务器的 API_KEY（推荐）

```bash
# 上游服务器
# 不设置 API_KEY（或设置一个简单的key）

# 下游服务器
export API_KEY=secure_key_for_downstream

# 结果：
# - 下游服务器的管理API受保护
# - 上游服务器调用下游管理API时，需要传递正确的 API_KEY
# - 如果上游服务器没有设置 API_KEY，调用会失败
```

#### 场景3：上下游都设置不同的 API_KEY（生产环境推荐）

```bash
# 上游服务器
export API_KEY=upstream_api_key_123

# 下游服务器
export API_KEY=downstream_api_key_456

# 结果：
# - 每个服务器的管理API都受保护
# - 上游服务器调用下游管理API时，需要传递下游服务器的 API_KEY
# - 需要在代码中配置下游服务器的 API_KEY（当前版本会自动传递自己的 API_KEY）
```

### 当前实现

当前版本中，当上游服务器调用下游服务器的管理API时：
- 如果上游服务器设置了 `API_KEY`，会自动传递这个 `API_KEY`
- 下游服务器会验证这个 `API_KEY` 是否匹配

**这意味着：如果上下游都设置了 `API_KEY`，它们需要相同才能调用管理API。**

## 4. 完整配置示例

### 上游服务器（Server A）

```bash
# 必需：TMDB API Key
export TMDB_API_KEY=your_tmdb_api_key

# 可选：应用API Key（保护管理API）
export API_KEY=shared_api_key_123

# 可选：手动配置其他上游服务器
export UPSTREAM_PROXY_SERVERS=http://server-b:6635,http://server-c:6635

# 服务器端口
export PORT=6635
```

### 下游服务器（Server B）

```bash
# 必需：TMDB API Key
export TMDB_API_KEY=your_tmdb_api_key

# 可选：应用API Key（保护管理API）
# 如果上游服务器要调用此服务器的管理API，需要设置相同的 API_KEY
export API_KEY=shared_api_key_123

# 服务器端口
export PORT=6636
```

## 5. 检测和验证

### 检查上游服务器检测状态

```bash
# 查看自动检测到的上游服务器
curl http://localhost:6635/api/upstream

# 响应示例：
{
  "enabled": true,
  "total_upstream_servers": 1,
  "tmdb_proxy_servers": 1,
  "upstream_servers": [
    {
      "url": "http://server-b:6636",
      "is_tmdb_proxy": true,
      "version": "1.0",
      "source": "auto_detected"
    }
  ]
}
```

### 测试管理API调用

```bash
# 测试调用下游服务器的管理API（需要 API_KEY）
curl -H "X-API-Key: shared_api_key_123" \
     http://server-b:6636/mapi/cache/info

# 如果 API_KEY 不匹配，会返回401错误
```

## 6. 常见问题

### Q: 为什么下游服务器检测不到上游服务器？

A: 可能的原因：
1. 上游服务器还未更新到此版本（没有 `X-TMDB-Proxy` 响应头）
2. 网络连接问题
3. 上游服务器未运行

**解决方案**：使用 `UPSTREAM_PROXY_SERVERS` 环境变量手动配置

### Q: 上下游服务器的 API_KEY 必须相同吗？

A: **当前版本**：如果上游服务器要调用下游服务器的管理API，需要相同的 `API_KEY`。

**未来改进**：可以添加 `UPSTREAM_API_KEY` 环境变量，专门用于调用上游服务器的管理API。

### Q: 不设置 API_KEY 会怎样？

A: 
- 管理API可以直接访问（不需要验证）
- 会有警告日志提示建议设置 `API_KEY`
- 适合开发环境，但不推荐生产环境

### Q: 如何让上游服务器调用下游服务器的管理API？

A: 当前实现中，上游服务器会自动传递自己的 `API_KEY`。因此：
- 如果下游服务器设置了 `API_KEY`，上游服务器也需要设置**相同的** `API_KEY`
- 或者下游服务器不设置 `API_KEY`（允许所有访问）

## 7. 最佳实践

### 开发环境

```bash
# 不设置 API_KEY，方便测试
# 只设置 TMDB_API_KEY
export TMDB_API_KEY=your_tmdb_api_key
```

### 生产环境

```bash
# 设置强密码的 API_KEY
export API_KEY=$(openssl rand -base64 32)

# 如果多个服务器需要相互调用管理API，使用相同的 API_KEY
# 或者考虑使用不同的 API_KEY，并通过其他方式（如VPN、防火墙）保护
```

### 多服务器部署

```bash
# 所有服务器使用相同的 API_KEY（用于相互调用）
export API_KEY=shared_production_key_xyz

# 每个服务器手动配置其他服务器
export UPSTREAM_PROXY_SERVERS=http://server1:6635,http://server2:6635,http://server3:6635
```

## 8. 总结

| 配置项 | 必需 | 说明 |
|--------|------|------|
| `TMDB_API_KEY` | ✅ 是 | TMDB官方API密钥 |
| `API_KEY` | ❌ 否 | 保护管理API，推荐生产环境设置 |
| `UPSTREAM_PROXY_SERVERS` | ❌ 否 | 手动配置上游服务器列表 |
| `X-TMDB-Proxy` 响应头 | ✅ 自动 | 系统自动添加，无需配置 |

**关键点**：
- ✅ 自动检测无需配置，系统会自动添加响应头
- ✅ `API_KEY` 是可选的，但推荐生产环境设置
- ⚠️ 如果上下游需要相互调用管理API，当前版本需要相同的 `API_KEY`
- ✅ 手动配置上游服务器列表用于聚合API功能
